package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	webui "github.com/user/bililive-recorder-autoarchive/frontend"
	"github.com/user/bililive-recorder-autoarchive/internal/app"
	"github.com/user/bililive-recorder-autoarchive/internal/config"
	ffmpegpkg "github.com/user/bililive-recorder-autoarchive/internal/ffmpeg"
	"github.com/user/bililive-recorder-autoarchive/internal/processor"
	"github.com/user/bililive-recorder-autoarchive/internal/scanner"
	"github.com/user/bililive-recorder-autoarchive/internal/storage"
	"github.com/user/bililive-recorder-autoarchive/internal/transcoder"
	"github.com/user/bililive-recorder-autoarchive/internal/webapi"
	"github.com/user/bililive-recorder-autoarchive/internal/webauth"
	"github.com/user/bililive-recorder-autoarchive/internal/webhook"
)

var (
	Version   = "dev"
	BuildTime = "unknown"
)

func main() {
	configPath := flag.String("config", "server.yaml", "YAML configuration file")
	bindAddress := flag.String("listen", "", "override HTTP bind address")
	port := flag.Int("port", 0, "override HTTP port")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("server %s (%s)\n", Version, BuildTime)
		return
	}

	absConfigPath, err := filepath.Abs(*configPath)
	if err != nil {
		log.Fatalf("resolve config path: %v", err)
	}
	cfg, err := config.Load(absConfigPath)
	if err != nil {
		log.Fatalf("load config %s: %v", absConfigPath, err)
	}
	cfg.FillDefaults()
	if *bindAddress != "" {
		cfg.Server.BindAddress = *bindAddress
	}
	if *port > 0 {
		cfg.Server.Port = *port
	}

	store, err := storage.New(storage.Config{DBPath: filepath.Join(filepath.Dir(absConfigPath), "data.db")})
	if err != nil {
		log.Fatalf("initialize storage: %v", err)
	}

	media := ffmpegpkg.New(ffmpegpkg.Config{
		FFmpegPath: cfg.FFmpeg.Path, FFprobePath: cfg.FFmpeg.FFprobePath,
		CustomArgs: cfg.FFmpeg.CustomArgs,
	})
	fileScanner := scanner.New(scanner.Config{
		InputDirFunc: func() string { return cfg.Processing.InputDir },
		Extensions:   []string{".flv"},
		MinFileSize:  cfg.Processing.MinFileSizeKB * 1024,
	})
	archiveProcessor, err := processor.New(processor.Config{
		MaxConcurrent: cfg.Processing.MaxConcurrent, InputDir: cfg.Processing.InputDir,
		OutputRoot: cfg.Processing.OutputRoot, DiscardDir: cfg.Processing.DiscardDir,
		PathTemplate: cfg.Rules.PathTemplate, CheckVideoStream: cfg.Processing.CheckVideoStream,
		MinFileSizeKB: cfg.Processing.MinFileSizeKB, DiscardFailedFiles: cfg.Processing.DiscardFailedFiles,
		ConflictMode: processor.ConflictMode(cfg.Processing.ConflictMode), DeleteOriginal: cfg.Processing.DeleteOriginal,
		FFmpeg: media, Storage: store, DefaultCoverPath: cfg.Covers.DefaultCover,
		Scanner: fileScanner, ScanInterval: time.Duration(cfg.Processing.ScanIntervalMin) * time.Minute,
	})
	if err != nil {
		_ = store.Close()
		log.Fatalf("initialize archive processor: %v", err)
	}

	tc := transcoder.New(transcoder.Config{
		FFmpegPath: cfg.FFmpeg.Path, FFprobePath: cfg.FFmpeg.FFprobePath,
		MaxWorkers: cfg.Transcode.MaxConcurrent,
	})
	tc.SetQSVReinitStrategy(transcoder.QSVReinitStrategy(cfg.Transcode.QSVReinitStrategy))
	tc.SetProcessLogCallback(func(entry transcoder.ProcessLogEntry) {
		if err := store.LogProcessResult(storage.ProcessLog{
			TaskID: entry.TaskID, InputPath: entry.InputPath, OutputPath: entry.OutputPath,
			Status: entry.Status, Error: entry.Error, StartTime: entry.StartTime, EndTime: entry.EndTime,
		}); err != nil {
			log.Printf("persist transcode result: %v", err)
		}
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := archiveProcessor.Start(ctx); err != nil {
		_ = store.Close()
		log.Fatalf("start archive processor: %v", err)
	}
	if err := tc.Start(ctx); err != nil {
		_ = archiveProcessor.Stop()
		_ = store.Close()
		log.Fatalf("start transcoder: %v", err)
	}

	appState := app.NewApp()
	appState.SetConfig(cfg)
	appState.SetConfigPath(absConfigPath)
	appState.SetProcessor(archiveProcessor)
	appState.SetStorage(store)
	appState.SetTranscoder(tc)

	var processorStopped atomic.Bool
	triggerScan := func() {
		go func() {
			groups, scanErr := fileScanner.Scan()
			if scanErr != nil {
				log.Printf("manual scan failed: %v", scanErr)
				return
			}
			for _, group := range groups {
				if addErr := archiveProcessor.AddTask(group.FLVPath); addErr != nil {
					log.Printf("add scanned file %s: %v", group.FLVPath, addErr)
				}
			}
		}()
	}
	appState.SetCallbacks(stop, triggerScan, func() {}, func() {})
	appState.SetShutdownCallbacks(func() {
		processorStopped.Store(true)
		stop()
	}, func(done func()) {
		go func() {
			archiveProcessor.StopGracefully()
			archiveProcessor.WaitForCompletion(30 * time.Minute)
			processorStopped.Store(true)
			done()
		}()
	})

	taskConfig := transcoder.TranscodeConfig{
		InputArgs: cfg.Transcode.InputArgs, QSVReinitStrategy: cfg.Transcode.QSVReinitStrategy,
		CustomArgs: cfg.Transcode.DefaultParams, OutputDir: cfg.Transcode.OutputDir,
		OutputExt: normalizeOutputExt(cfg.Transcode.DefaultFormat), MaxFPS: cfg.Transcode.MaxFPS,
		DeleteSourceOnSuccess: cfg.Transcode.DeleteSourceOnSuccess,
	}
	httpServer := webhook.New(webhook.Config{
		BindAddress: cfg.Server.BindAddress, APIToken: cfg.Server.APIToken,
		Port: cfg.Server.Port, WebhookPath: cfg.Server.WebhookPath,
		InputDir: cfg.Processing.InputDir, MaxFPS: cfg.Transcode.MaxFPS,
		DefaultTranscode: taskConfig,
	})
	auth, err := webauth.New(filepath.Join(filepath.Dir(absConfigPath), "server-auth.json"))
	if err != nil {
		log.Fatalf("initialize web authentication: %v", err)
	}
	httpServer.SetAuth(auth)
	httpServer.SetApp(appState)
	httpServer.On(webhook.EventFileClosed, func(event *webhook.Event) error {
		if !cfg.Server.WebhookEnabled {
			return nil
		}
		fullPath, pathErr := httpServer.GetFullPath(event.EventData.RelativePath)
		if pathErr != nil {
			return pathErr
		}
		return archiveProcessor.AddTask(fullPath)
	})
	assets, err := fs.Sub(webui.Assets, ".")
	if err != nil {
		log.Fatalf("initialize web UI: %v", err)
	}
	fileServer := http.FileServer(http.FS(assets))
	httpServer.SetWebUI(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			clone := r.Clone(r.Context())
			urlCopy := *r.URL
			urlCopy.Path = "/login.html"
			clone.URL = &urlCopy
			fileServer.ServeHTTP(w, clone)
			return
		}
		fileServer.ServeHTTP(w, r)
	}))
	httpServer.SetAppAPI(webapi.New(appState))

	serverErr := make(chan error, 1)
	go func() { serverErr <- httpServer.Start() }()
	log.Printf("server %s (%s) started with archive webhook and browser UI", Version, BuildTime)
	select {
	case <-ctx.Done():
	case serveErr := <-serverErr:
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			log.Printf("HTTP server stopped: %v", serveErr)
		}
		stop()
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Stop(shutdownCtx)
	_ = tc.Stop()
	if !processorStopped.Load() {
		_ = archiveProcessor.Stop()
	}
	_ = store.Close()
}

func normalizeOutputExt(format string) string {
	format = strings.TrimSpace(format)
	if format == "" {
		return ".mp4"
	}
	if strings.HasPrefix(format, ".") {
		return format
	}
	return "." + format
}
