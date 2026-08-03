package processor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	ffmpegpkg "github.com/user/bililive-recorder-autoarchive/internal/ffmpeg"
	"github.com/user/bililive-recorder-autoarchive/internal/scanner"
)

type probeOnlyFFmpeg struct{ duration float64 }

func (f probeOnlyFFmpeg) Remux(context.Context, string, string, *ffmpegpkg.RemuxOptions) error {
	return nil
}
func (f probeOnlyFFmpeg) HasVideoStream(context.Context, string) (bool, error) { return true, nil }
func (f probeOnlyFFmpeg) Probe(context.Context, string) (*ffmpegpkg.MediaInfo, error) {
	return &ffmpegpkg.MediaInfo{Duration: f.duration, VideoStream: &ffmpegpkg.StreamInfo{Width: 1920, Height: 1080}}, nil
}

func writeGroup(t *testing.T, dir string) scanner.FileGroup {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	stem := filepath.Join(dir, "录制-123-20260803-120000")
	for path, data := range map[string][]byte{stem + ".flv": []byte("video"), stem + ".xml": []byte("xml"), stem + ".cover.jpg": []byte("jpg")} {
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	return scanner.FileGroup{FLVPath: stem + ".flv", XMLPath: stem + ".xml", CoverPath: stem + ".cover.jpg", StreamerName: "anchor", StreamerDir: "123-anchor", StreamerUID: "123"}
}

func TestMoveModeStagesWholeGroup(t *testing.T) {
	root := t.TempDir()
	group := writeGroup(t, filepath.Join(root, "rec", "123-anchor"))
	p, err := New(Config{OutputRoot: filepath.Join(root, "waiting"), PathTemplate: "{{.OutputDir}}", StagingMode: "move", FFmpeg: probeOnlyFFmpeg{duration: 10}, CheckVideoStream: true, MinDurationSec: 1, ConflictMode: ConflictRename})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.ProcessGroup(group); err != nil {
		t.Fatal(err)
	}
	stem := filepath.Join(root, "waiting", "录制-123-20260803-120000")
	if filepath.Ext(stem+".flv") != ".flv" {
		t.Fatal("move mode changed container")
	}
	for _, path := range []string{stem + ".flv", stem + ".xml", stem + ".cover.jpg"} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("staged companion missing %s: %v", path, err)
		}
	}
}

func TestMinDurationDiscardsWholeGroup(t *testing.T) {
	root := t.TempDir()
	group := writeGroup(t, filepath.Join(root, "rec", "123-anchor"))
	discard := filepath.Join(root, "discard")
	p, err := New(Config{OutputRoot: filepath.Join(root, "waiting"), DiscardDir: discard, PathTemplate: "{{.OutputDir}}", StagingMode: "move", FFmpeg: probeOnlyFFmpeg{duration: .2}, MinDurationSec: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.ProcessGroup(group); err != ErrDiscarded {
		t.Fatalf("got %v, want ErrDiscarded", err)
	}
	for _, name := range []string{filepath.Base(group.FLVPath), filepath.Base(group.XMLPath), filepath.Base(group.CoverPath)} {
		if _, err := os.Stat(filepath.Join(discard, "anchor", name)); err != nil {
			t.Fatalf("discarded group missing %s: %v", name, err)
		}
	}
}

func TestDiscardDoesNotMoveGlobalDefaultCover(t *testing.T) {
	root := t.TempDir()
	group := writeGroup(t, filepath.Join(root, "rec", "123-anchor"))
	if err := os.Remove(group.CoverPath); err != nil {
		t.Fatal(err)
	}
	defaultCover := filepath.Join(root, "default.jpg")
	if err := os.WriteFile(defaultCover, []byte("default"), 0644); err != nil {
		t.Fatal(err)
	}
	group.CoverPath = defaultCover
	p, err := New(Config{DiscardDir: filepath.Join(root, "discard")})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.discardGroup(group, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(defaultCover); err != nil {
		t.Fatalf("global default cover was moved: %v", err)
	}
}

func TestOrphanCleanupUsesPhysicalVideoPresence(t *testing.T) {
	root := t.TempDir()
	rec := filepath.Join(root, "rec", "123-anchor")
	if err := os.MkdirAll(rec, 0755); err != nil {
		t.Fatal(err)
	}
	pairedCover := filepath.Join(rec, "paired.cover.jpg")
	orphanXML := filepath.Join(rec, "orphan.xml")
	for _, path := range []string{pairedCover, filepath.Join(rec, "paired.flv"), orphanXML} {
		if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-2 * time.Hour)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	p, err := New(Config{InputDir: filepath.Join(root, "rec"), DiscardDir: filepath.Join(root, "discard"), OrphanGrace: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.cleanupOrphans(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pairedCover); err != nil {
		t.Fatalf("cover with physical video was moved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "discard", "orphan", "123-anchor", "orphan.xml")); err != nil {
		t.Fatalf("true orphan not moved: %v", err)
	}
}
