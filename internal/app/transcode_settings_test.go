package app

import (
	"path/filepath"
	"testing"

	"github.com/user/bililive-recorder-autoarchive/internal/config"
)

func TestSaveTranscodeSettingsNotifiesAutomaticPipeline(t *testing.T) {
	application := NewApp()
	application.SetConfig(config.Default())
	application.SetConfigPath(filepath.Join(t.TempDir(), "server.yaml"))

	var received TranscodeSettings
	application.SetTranscodeSettingsSavedCallback(func(settings TranscodeSettings) {
		received = settings
	})
	want := TranscodeSettings{
		Params:            "-c:v av1_qsv",
		Format:            "mp4",
		InputArgs:         "-hwaccel qsv -hwaccel_output_format qsv",
		PreserveCover:     true,
		MaxFPS:            30,
		LimitResolution:   true,
		QSVReinitStrategy: "nv12",
	}
	if err := application.SaveTranscodeSettings(want); err != nil {
		t.Fatal(err)
	}
	if received != want {
		t.Fatalf("callback settings = %#v, want %#v", received, want)
	}
	if !application.config.Transcode.LimitResolution {
		t.Fatal("limit_resolution was not persisted in memory")
	}
}
