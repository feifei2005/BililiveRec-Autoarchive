package transcoder

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseResolutionSegmentsNormalizesNonZeroTimestamps(t *testing.T) {
	probe := []byte(`{
		"frames": [
			{"best_effort_timestamp_time":"1.480000","width":320,"height":240},
			{"best_effort_timestamp_time":"2.480000","width":320,"height":240},
			{"best_effort_timestamp_time":"3.480000","width":640,"height":360},
			{"best_effort_timestamp_time":"4.480000","width":640,"height":360}
		],
		"format":{"duration":"4.000000"}
	}`)

	segments, err := parseResolutionSegments(probe, 4)
	if err != nil {
		t.Fatalf("parseResolutionSegments() error = %v", err)
	}
	assertResolutionSegments(t, segments, []resolutionSegment{
		{startTime: 0, endTime: 2, width: 320, height: 240},
		{startTime: 2, endTime: 4, width: 640, height: 360},
	})
}

func TestParseResolutionSegmentsUsesProbeDurationAndPTSFallback(t *testing.T) {
	probe := []byte(`{
		"frames": [
			{"pts_time":"10.000000","width":1920,"height":1080},
			{"pts_time":"15.000000","width":1280,"height":720}
		],
		"format":{"duration":"8.000000"}
	}`)

	segments, err := parseResolutionSegments(probe, 0)
	if err != nil {
		t.Fatalf("parseResolutionSegments() error = %v", err)
	}
	assertResolutionSegments(t, segments, []resolutionSegment{
		{startTime: 0, endTime: 5, width: 1920, height: 1080},
		{startTime: 5, endTime: 8, width: 1280, height: 720},
	})
}

func TestParseResolutionSegmentsDoesNotCreateZeroLengthSegments(t *testing.T) {
	probe := []byte(`{
		"frames": [
			{"best_effort_timestamp_time":"20.000000","width":320,"height":240},
			{"best_effort_timestamp_time":"20.000000","width":640,"height":360},
			{"best_effort_timestamp_time":"24.000000","width":640,"height":360},
			{"best_effort_timestamp_time":"30.000000","width":1280,"height":720}
		],
		"format":{"duration":"6.000000"}
	}`)

	segments, err := parseResolutionSegments(probe, 6)
	if err != nil {
		t.Fatalf("parseResolutionSegments() error = %v", err)
	}
	assertResolutionSegments(t, segments, []resolutionSegment{
		{startTime: 0, endTime: 6, width: 640, height: 360},
	})
}

func TestParseResolutionSegmentsRejectsUnknownDuration(t *testing.T) {
	probe := []byte(`{"frames":[{"best_effort_timestamp_time":"0","width":320,"height":240}]}`)
	_, err := parseResolutionSegments(probe, 0)
	if err == nil || !strings.Contains(err.Error(), "无法确定视频总时长") {
		t.Fatalf("parseResolutionSegments() error = %v, want duration error", err)
	}
}

func TestBuildSegmentTranscodeArgsUsesDurationAndFPSFilter(t *testing.T) {
	task := &TranscodeTask{
		InputPath: "input.ts",
		FrameRate: 60,
		Config: TranscodeConfig{
			InputArgs:  "-hwaccel qsv -hwaccel_output_format qsv",
			CustomArgs: "-c:v h264_qsv -c:a aac -f mp4",
			MaxFPS:     30,
		},
	}
	args := buildSegmentTranscodeArgs(task, nil, resolutionSegment{
		startTime: 2,
		endTime:   4.5,
		width:     640,
		height:    360,
	}, "segment.mp4")
	joined := strings.Join(args, " ")

	for _, expected := range []string{
		"-ss 2.000000 -i input.ts",
		"-t 2.500000",
		"-map 0:v:0 -map 0:a:0?",
		"-filter:v:0 fps=fps=30",
		"-avoid_negative_ts make_zero segment.mp4",
	} {
		if !strings.Contains(joined, expected) {
			t.Errorf("args %q do not contain %q", joined, expected)
		}
	}
	if strings.Contains(joined, " -to ") {
		t.Errorf("args %q unexpectedly use absolute -to", joined)
	}
}

func TestIsQSVReinitError(t *testing.T) {
	if isQSVReinitError(nil) {
		t.Fatal("nil error was classified as a QSV reinitialization error")
	}
	if !isQSVReinitError(errors.New("Error reinitializing filters! Error code -40")) {
		t.Fatal("expected case-insensitive FFmpeg error to be recognized")
	}
	if isQSVReinitError(errors.New("Error reinitializing filters: invalid argument")) {
		t.Fatal("unrelated filter error was classified as a QSV reinitialization error")
	}
}

func TestResolutionSegmentPipelineIntegration(t *testing.T) {
	if os.Getenv("RUN_FFMPEG_INTEGRATION") == "" {
		t.Skip("set RUN_FFMPEG_INTEGRATION=1 to run the FFmpeg integration test")
	}
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ffprobePath, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is not installed")
	}

	tempDir := t.TempDir()
	firstPath := filepath.Join(tempDir, "first.ts")
	secondPath := filepath.Join(tempDir, "second.ts")
	inputPath := filepath.Join(tempDir, "variable.ts")
	outputPath := filepath.Join(tempDir, "output.mp4")
	runFFmpegTestCommand(t, ffmpegPath,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=25:duration=2",
		"-c:v", "libx264", "-g", "25", "-f", "mpegts", firstPath,
	)
	runFFmpegTestCommand(t, ffmpegPath,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=25:duration=2",
		"-c:v", "libx264", "-g", "25", "-f", "mpegts", secondPath,
	)
	concatInput := fmt.Sprintf("concat:%s|%s", filepath.ToSlash(firstPath), filepath.ToSlash(secondPath))
	runFFmpegTestCommand(t, ffmpegPath,
		"-hide_banner", "-loglevel", "error", "-y",
		"-i", concatInput, "-c", "copy", inputPath,
	)

	transcoder := New(Config{FFmpegPath: ffmpegPath, FFprobePath: ffprobePath, MaxWorkers: 1})
	transcoder.ctx = context.Background()
	inputSegments, err := transcoder.findResolutionSegments(inputPath, 4)
	if err != nil {
		t.Fatalf("findResolutionSegments(input) error = %v", err)
	}
	assertResolutionSegments(t, inputSegments, []resolutionSegment{
		{startTime: 0, endTime: 2, width: 320, height: 240},
		{startTime: 2, endTime: 4, width: 640, height: 360},
	})

	task := &TranscodeTask{
		InputPath:   inputPath,
		OutputPath:  outputPath,
		Duration:    4,
		TotalFrames: 100,
		Config: TranscodeConfig{
			CustomArgs: "-c:v libx264 -preset ultrafast -c:a aac -f mp4",
		},
	}
	if err := transcoder.transcodeWithSegments(task, &VideoFile{}); err != nil {
		t.Fatalf("transcodeWithSegments() error = %v", err)
	}
	if task.OutputPath != filepath.Join(tempDir, "output_1.mp4") {
		t.Fatalf("task output path = %q, want first independent segment", task.OutputPath)
	}

	wantOutputs := []struct {
		width  int
		height int
	}{
		{width: 320, height: 240},
		{width: 640, height: 360},
	}
	for i, want := range wantOutputs {
		segmentPath := filepath.Join(tempDir, fmt.Sprintf("output_%d.mp4", i+1))
		if _, err := os.Stat(segmentPath); err != nil {
			t.Fatalf("segment %d missing: %v", i+1, err)
		}
		outputSegments, err := transcoder.findResolutionSegments(segmentPath, 0)
		if err != nil {
			t.Fatalf("findResolutionSegments(segment %d) error = %v", i+1, err)
		}
		if len(outputSegments) != 1 || outputSegments[0].width != want.width || outputSegments[0].height != want.height {
			t.Fatalf("segment %d metadata = %#v, want %dx%d", i+1, outputSegments, want.width, want.height)
		}
		outputInfo, err := transcoder.probeVideo(segmentPath)
		if err != nil {
			t.Fatalf("probeVideo(segment %d) error = %v", i+1, err)
		}
		if math.Abs(outputInfo.Duration-2) > 0.25 {
			t.Fatalf("segment %d duration = %.3fs, want approximately 2s", i+1, outputInfo.Duration)
		}
	}
}

func TestSetMaxWorkersRapidResizeDoesNotDuplicateWorkers(t *testing.T) {
	transcoder := New(Config{MaxWorkers: 1})
	if err := transcoder.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer func() {
		if err := transcoder.Stop(); err != nil {
			t.Errorf("Stop() error = %v", err)
		}
	}()

	transcoder.SetMaxWorkers(4)
	transcoder.SetMaxWorkers(1)
	transcoder.SetMaxWorkers(3)
	waitForActiveWorkers(t, transcoder, 3)

	transcoder.SetMaxWorkers(1)
	waitForActiveWorkers(t, transcoder, 1)
}

func TestWorkerSupervisorRestoresMissingWorker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	transcoder := New(Config{MaxWorkers: 1})
	transcoder.ctx = ctx
	transcoder.workersMu.Lock()
	transcoder.workersStarted = true
	transcoder.activeWorkers = 0
	transcoder.workersMu.Unlock()
	transcoder.wg.Add(1)
	go transcoder.workerSupervisor()
	waitForActiveWorkers(t, transcoder, 1)
	cancel()
	done := make(chan struct{})
	go func() { transcoder.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("supervised worker did not stop")
	}
}

func TestBuildQSVNV12InputArgsForcesIntelDecode(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "qsv surfaces", input: "-hwaccel qsv -hwaccel_output_format qsv", want: "-hwaccel qsv -hwaccel_output_format nv12"},
		{name: "missing options", input: "-qsv_device /dev/dri/renderD128", want: "-hwaccel qsv -qsv_device /dev/dri/renderD128 -hwaccel_output_format nv12"},
		{name: "replace other accelerator", input: "-hwaccel cuda -hwaccel_output_format cuda", want: "-hwaccel qsv -hwaccel_output_format nv12"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := strings.Join(buildQSVNV12InputArgs(tt.input), " ")
			if got != tt.want {
				t.Fatalf("buildQSVNV12InputArgs(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNV12FallbackPoolIsSingleConcurrency(t *testing.T) {
	transcoder := New(Config{MaxWorkers: 1})
	started := make(chan string, 2)
	release := make(chan struct{}, 2)
	var active atomic.Int32
	var maximum atomic.Int32
	transcoder.nv12FallbackRunner = func(task *TranscodeTask, _ *VideoFile) error {
		current := active.Add(1)
		for {
			old := maximum.Load()
			if current <= old || maximum.CompareAndSwap(old, current) {
				break
			}
		}
		started <- task.ID
		<-release
		active.Add(-1)
		return nil
	}

	if err := transcoder.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer func() {
		if err := transcoder.Stop(); err != nil {
			t.Errorf("Stop() error = %v", err)
		}
	}()

	task1 := &TranscodeTask{ID: "fallback-1", Status: StatusProcessing, StartedAt: time.Now()}
	task2 := &TranscodeTask{ID: "fallback-2", Status: StatusProcessing, StartedAt: time.Now()}
	if err := transcoder.enqueueNV12Fallback(task1, nil, errors.New("qsv failed")); err != nil {
		t.Fatalf("enqueue first fallback: %v", err)
	}
	if err := transcoder.enqueueNV12Fallback(task2, nil, errors.New("qsv failed")); err != nil {
		t.Fatalf("enqueue second fallback: %v", err)
	}

	select {
	case id := <-started:
		if id != task1.ID {
			t.Fatalf("first fallback = %s, want %s", id, task1.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("first fallback did not start")
	}
	select {
	case id := <-started:
		t.Fatalf("fallback %s started concurrently", id)
	case <-time.After(100 * time.Millisecond):
	}

	release <- struct{}{}
	select {
	case id := <-started:
		if id != task2.ID {
			t.Fatalf("second fallback = %s, want %s", id, task2.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("second fallback did not start")
	}
	release <- struct{}{}

	waitForTaskStatus(t, transcoder, task1, StatusSuccess)
	waitForTaskStatus(t, transcoder, task2, StatusSuccess)
	if got := maximum.Load(); got != 1 {
		t.Fatalf("maximum NV12 fallback concurrency = %d, want 1", got)
	}
	if task1.ExecutionPool != "nv12" || task2.ExecutionPool != "nv12" {
		t.Fatalf("fallback execution pools = %q, %q", task1.ExecutionPool, task2.ExecutionPool)
	}
}

func waitForTaskStatus(t *testing.T, transcoder *Transcoder, task *TranscodeTask, want TaskStatus) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		transcoder.mu.RLock()
		got := task.Status
		transcoder.mu.RUnlock()
		if got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	transcoder.mu.RLock()
	got := task.Status
	transcoder.mu.RUnlock()
	t.Fatalf("task status = %s, want %s", got, want)
}

func waitForActiveWorkers(t *testing.T, transcoder *Transcoder, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		transcoder.workersMu.Lock()
		got := transcoder.activeWorkers
		transcoder.workersMu.Unlock()
		if got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	transcoder.workersMu.Lock()
	got := transcoder.activeWorkers
	transcoder.workersMu.Unlock()
	t.Fatalf("active workers = %d, want %d", got, want)
}

func runFFmpegTestCommand(t *testing.T, ffmpegPath string, args ...string) {
	t.Helper()
	output, err := exec.Command(ffmpegPath, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func assertResolutionSegments(t *testing.T, got, want []resolutionSegment) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("segment count = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("segment %d = %#v, want %#v", i, got[i], want[i])
		}
	}
}
