package config

import "testing"

func TestQSVReinitStrategyDefaultsToNV12(t *testing.T) {
	cfg := Config{}
	cfg.FillDefaults()

	if got := cfg.Transcode.QSVReinitStrategy; got != "nv12" {
		t.Fatalf("QSVReinitStrategy = %q, want nv12", got)
	}
}

func TestLegacySegmentStrategyMigratesToNV12(t *testing.T) {
	cfg := Config{Transcode: TranscodeConfig{QSVReinitStrategy: "segment"}}
	cfg.FillDefaults()

	if got := cfg.Transcode.QSVReinitStrategy; got != "nv12" {
		t.Fatalf("QSVReinitStrategy = %q, want nv12", got)
	}
}

func TestLegacyAMDDefaultsMigrateToIntelQSV(t *testing.T) {
	cfg := Config{Transcode: TranscodeConfig{
		DefaultParams: "-c:v av1_amf -profile:v main -rc:v cqp -qp_i 130 -qp_p 130 -c:a libopus",
	}}
	cfg.FillDefaults()

	if got := cfg.Transcode.DefaultParams; got != Default().Transcode.DefaultParams {
		t.Fatalf("DefaultParams = %q, want %q", got, Default().Transcode.DefaultParams)
	}
	if got := cfg.Transcode.InputArgs; got != "-hwaccel qsv -hwaccel_output_format qsv" {
		t.Fatalf("InputArgs = %q, want Intel QSV input", got)
	}
}
