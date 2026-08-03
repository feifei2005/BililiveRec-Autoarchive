package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanWaitingIsIndependentFileConsumer(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "anchor", "2026", "08", "04")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	stem := filepath.Join(dir, "recording")
	for _, path := range []string{stem + ".flv", stem + ".xml", stem + ".cover.jpg", filepath.Join(dir, ".partial.transcoding.mp4")} {
		if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	var groups []waitingGroup
	if err := scanWaiting(root, func(group waitingGroup) error { groups = append(groups, group); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1", len(groups))
	}
	if groups[0].VideoPath != stem+".flv" || groups[0].XMLPath != stem+".xml" || groups[0].CoverPath != stem+".cover.jpg" {
		t.Fatalf("wrong waiting group: %+v", groups[0])
	}
}

func TestFinalOutputPathRenamesExistingFile(t *testing.T) {
	root := t.TempDir()
	waiting := filepath.Join(root, "waiting")
	output := filepath.Join(root, "output")
	input := filepath.Join(waiting, "anchor", "video.flv")
	if err := os.MkdirAll(filepath.Dir(input), 0755); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(output, "anchor", "video.mp4")
	if err := os.MkdirAll(filepath.Dir(existing), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(existing, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := finalOutputPath(waiting, output, input, ".mp4", "rename")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(output, "anchor", "video_1.mp4")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
