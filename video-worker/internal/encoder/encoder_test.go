package encoder

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBuildFFmpegArgs(t *testing.T) {
	config := Config{Source: "/videos/originals/matrix.mp4", Resolution: 720, Output: "/videos/encoded/matrix/720"}
	args, err := BuildFFmpegArgs(config)
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{"-nostdin", "-y", "-i", config.Source, "-vf", "scale=-2:720", "-c:v", "libx264", "-c:a", "aac", "-hls_time", "6", "-hls_playlist_type", "vod", "-hls_segment_filename", config.Output + "/segment_%03d.ts", config.Output + "/index.m3u8"}
	if !reflect.DeepEqual(args, expected) {
		t.Fatalf("Unexpected arguments: %q", args)
	}
	config.Source = "/videos/originals/a file;$(echo bad).mp4"
	args, err = BuildFFmpegArgs(config)
	if err != nil || args[3] != config.Source {
		t.Fatalf("Source must remain one literal argument: %v, %v", args, err)
	}
}

func TestInvalidResolution(t *testing.T) {
	for _, resolution := range []int{-1, 0, 480, 721} {
		if _, err := BuildFFmpegArgs(Config{Source: "matrix.mp4", Resolution: resolution, Output: "output"}); err == nil {
			t.Fatalf("Accepted resolution %d", resolution)
		}
	}
}

func TestConfigFromEnv(t *testing.T) {
	env := map[string]string{"SOURCE": "/videos/originals/matrix.mp4", "RESOLUTION": "720", "OUTPUT": "/videos/encoded/matrix/720"}
	config, err := ConfigFromEnv(func(name string) string { return env[name] })
	if err != nil || config.Resolution != 720 {
		t.Fatalf("Unexpected config: %+v, %v", config, err)
	}
	for _, name := range []string{"SOURCE", "RESOLUTION", "OUTPUT"} {
		t.Run(name, func(t *testing.T) {
			_, err := ConfigFromEnv(func(key string) string {
				if key == name {
					return ""
				}
				return env[key]
			})
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("Expected missing %s error, got %v", name, err)
			}
		})
	}
	env["RESOLUTION"] = "invalid"
	if _, err := ConfigFromEnv(func(name string) string { return env[name] }); err == nil {
		t.Fatal("Accepted nonnumeric resolution")
	}
}

func TestEncodeReportsMissingInput(t *testing.T) {
	err := Encode(context.Background(), Config{Source: filepath.Join(t.TempDir(), "missing.mp4"), Resolution: 720, Output: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "SOURCE") {
		t.Fatalf("Expected source error: %v", err)
	}
}

// A fake executable exercises process failure without installing or running FFmpeg.
func TestEncodeReportsProcessFailure(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "matrix.mp4")
	if err := os.WriteFile(source, []byte("test input"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ffmpeg"), []byte("#!/bin/sh\nexit 7\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	output := filepath.Join(dir, "output", "720")
	err := Encode(context.Background(), Config{Source: source, Resolution: 720, Output: output})
	if err == nil || !strings.Contains(err.Error(), "exit status 7") {
		t.Fatalf("Expected process failure: %v", err)
	}
	if _, err := os.Stat(output); err != nil {
		t.Fatalf("Output directory was not created: %v", err)
	}
}
