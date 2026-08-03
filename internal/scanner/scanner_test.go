package scanner

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanDoesNotSilentlySkipSmallFLV(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "123-anchor")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	video := filepath.Join(dir, "录制-123-20260803-120000.flv")
	if err := os.WriteFile(video, []byte("tiny"), 0644); err != nil {
		t.Fatal(err)
	}

	s := New(Config{InputDirFunc: func() string { return root }, MinFileSize: 1 << 30})
	groups, err := s.Scan()
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].FLVPath != video {
		t.Fatalf("small FLV must reach processor, groups=%v", groups)
	}
}
