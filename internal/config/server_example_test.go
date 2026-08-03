package config

import (
	"path/filepath"
	"testing"
)

func TestServerExampleUsesSafeThreeStagePipeline(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "configs", "server.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.FillDefaults()
	if cfg.Processing.InputDir != "./rec" || cfg.Processing.OutputRoot != "./waiting" || cfg.Transcode.OutputDir != "./output" {
		t.Fatalf("unexpected pipeline paths: rec=%q waiting=%q output=%q", cfg.Processing.InputDir, cfg.Processing.OutputRoot, cfg.Transcode.OutputDir)
	}
	if cfg.Processing.StagingMode != "move" || cfg.Processing.MinDurationSec <= 0 {
		t.Fatalf("unsafe processing defaults: %+v", cfg.Processing)
	}
}
