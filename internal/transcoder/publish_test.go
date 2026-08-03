package transcoder

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExternalCoverUsesWorkingOutput(t *testing.T) {
	task := &TranscodeTask{InputPath: "video.flv", OutputPath: "output/video.mp4", WorkingOutputPath: "waiting/.video.transcoding.mp4", Config: TranscodeConfig{CustomArgs: "-c:v av1_qsv -c:a aac", ExternalCoverPath: "video.cover.jpg"}}
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
