package transcoder

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExternalCoverUsesWorkingOutput(t *testing.T) {
	task := &TranscodeTask{InputPath: "video.flv", OutputPath: "output/video.mp4", WorkingOutputPath: "waiting/.video.transcoding.mp4", Config: TranscodeConfig{CustomArgs: "-c:v av1_qsv -c:a aac", ExternalCoverPath: "video.cover.jpg", PreserveCover: true}}
	args := (&Transcoder{}).buildFFmpegArgs(task, nil)
	want := []string{"-i", "video.cover.jpg", "-map", "1:v:0", "-disposition:v:1", "attached_pic", "waiting/.video.transcoding.mp4"}
	for _, item := range want {
		found := false
		for _, arg := range args {
			if arg == item {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing %q in args %v", item, args)
		}
	}
}

func TestEmbeddedCoverWinsOverExternalCover(t *testing.T) {
	task := &TranscodeTask{InputPath: "video.mkv", OutputPath: "video.mp4", Config: TranscodeConfig{CustomArgs: "-c:v av1_qsv -c:a aac", ExternalCoverPath: "video.cover.jpg", PreserveCover: true}}
	info := &VideoFile{VideoIndex: 0, AudioIndex: 1, HasCover: true, CoverIndex: 2}
	args := (&Transcoder{}).buildFFmpegArgs(task, info)
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "video.cover.jpg") {
		t.Fatalf("external cover was added beside embedded cover: %v", args)
	}
	if !strings.Contains(joined, "-map 0:2") || !strings.Contains(joined, "-disposition:v:1 attached_pic") {
		t.Fatalf("embedded cover not preserved: %v", args)
	}
}

func TestDisableCoverDropsEmbeddedAndExternalCover(t *testing.T) {
	task := &TranscodeTask{InputPath: "video.mkv", OutputPath: "video.mp4", Config: TranscodeConfig{CustomArgs: "-c:v av1_qsv -c:a aac", ExternalCoverPath: "video.cover.jpg", PreserveCover: false}}
	info := &VideoFile{VideoIndex: 0, AudioIndex: 1, HasCover: true, CoverIndex: 2}
	joined := strings.Join((&Transcoder{}).buildFFmpegArgs(task, info), " ")
	if strings.Contains(joined, "video.cover.jpg") || strings.Contains(joined, "0:2") || strings.Contains(joined, "attached_pic") {
		t.Fatalf("cover was kept despite PreserveCover=false: %s", joined)
	}
}

func TestVideoQualityOptionsDoNotLeakToOpus(t *testing.T) {
	args := normalizeVideoStreamSelectors(strings.Fields("-c:v av1_qsv -global_quality 23 -look_ahead 1 -c:a libopus -b:a 96k"))
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-global_quality:v:0 23") || !strings.Contains(joined, "-look_ahead:v:0 1") {
		t.Fatalf("video options are not stream-qualified: %s", joined)
	}
	if strings.Contains(joined, " -global_quality 23") {
		t.Fatalf("global quality can leak to audio: %s", joined)
	}
}

func TestAddTaskDeduplicatesUnchangedPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "video.mkv")
	if err := os.WriteFile(path, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}
	tr := New(Config{})
	first, err := tr.AddTask(path, TranscodeConfig{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := tr.AddTask(path, TranscodeConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if first != second || len(tr.tasks) != 1 || len(tr.taskQueue) != 1 {
		t.Fatalf("duplicate task was queued: tasks=%d queue=%d", len(tr.tasks), len(tr.taskQueue))
	}
}

func TestPublishMovesCompletedFileAndSidecar(t *testing.T) {
	root := t.TempDir()
	working := filepath.Join(root, "waiting", ".video.transcoding.mp4")
	xml := filepath.Join(root, "waiting", "video.xml")
	final := filepath.Join(root, "output", "video.mp4")
	if err := os.MkdirAll(filepath.Dir(working), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(working, []byte("complete"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(xml, []byte("xml"), 0644); err != nil {
		t.Fatal(err)
	}
	task := &TranscodeTask{ID: "test", InputPath: filepath.Join(root, "waiting", "video.flv"), OutputPath: final, WorkingOutputPath: working, Config: TranscodeConfig{PublishAfterSuccess: true, SidecarXMLPath: xml}}
	if err := (&Transcoder{}).publishTaskOutput(task); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(final); err != nil || string(data) != "complete" {
		t.Fatalf("final output invalid: %q %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(root, "output", "video.xml")); err != nil {
		t.Fatalf("XML not published: %v", err)
	}
	if _, err := os.Stat(working); !os.IsNotExist(err) {
		t.Fatalf("working file still exists: %v", err)
	}
}
