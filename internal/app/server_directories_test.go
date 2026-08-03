package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/user/bililive-recorder-autoarchive/internal/config"
)

func TestListServerDirectoriesDefaultsToWaiting(t *testing.T) {
	root := t.TempDir()
	waiting := filepath.Join(root, "waiting")
	child := filepath.Join(waiting, "anchor")
	if err := os.MkdirAll(child, 0755); err != nil {
		t.Fatal(err)
	}
	app := &App{config: &config.Config{Processing: config.ProcessingConfig{OutputRoot: waiting}}}
	listing, err := app.ListServerDirectories("")
	if err != nil {
		t.Fatal(err)
	}
	if listing.Current != waiting || len(listing.Dirs) != 1 || listing.Dirs[0] != child {
		t.Fatalf("unexpected listing: %+v", listing)
	}
}
