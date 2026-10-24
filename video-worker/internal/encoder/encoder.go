// Package encoder runs FFmpeg to encode one resolution as HLS VOD.
package encoder

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Config identifies one original, resolution and output directory.
type Config struct {
	Source     string
	Resolution int
	Output     string
}

// ConfigFromEnv reads and validates the worker's required environment variables.
func ConfigFromEnv(getenv func(string) string) (Config, error) {
	for _, name := range []string{"SOURCE", "RESOLUTION", "OUTPUT"} {
		if strings.TrimSpace(getenv(name)) == "" {
			return Config{}, fmt.Errorf("required environment variable %s is missing or empty", name)
		}
	}
	resolution, err := strconv.Atoi(getenv("RESOLUTION"))
	if err != nil {
		return Config{}, fmt.Errorf("invalid RESOLUTION %q: %w", getenv("RESOLUTION"), err)
	}
	config := Config{Source: getenv("SOURCE"), Resolution: resolution, Output: getenv("OUTPUT")}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

// Validate checks the supported resolutions and required paths.
func (config Config) Validate() error {
	if strings.TrimSpace(config.Source) == "" {
		return fmt.Errorf("SOURCE must not be empty")
	}
	if strings.TrimSpace(config.Output) == "" {
		return fmt.Errorf("OUTPUT must not be empty")
	}
	switch config.Resolution {
	case 360, 720, 1080:
		return nil
	default:
		return fmt.Errorf("unsupported RESOLUTION %d: expected 360, 720 or 1080", config.Resolution)
	}
}

// BuildFFmpegArgs constructs arguments directly; source paths never enter a shell.
func BuildFFmpegArgs(config Config) ([]string, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return []string{
		// Overwrite partial outputs on Job retry; never wait for interactive input.
		"-nostdin", "-y",
		"-i", config.Source,
		"-vf", fmt.Sprintf("scale=-2:%d", config.Resolution),
		"-c:v", "libx264",
		"-c:a", "aac",
		"-hls_time", "6",
		"-hls_playlist_type", "vod",
		"-hls_segment_filename", filepath.Join(config.Output, "segment_%03d.ts"),
		filepath.Join(config.Output, "index.m3u8"),
	}, nil
}

// Encode creates the output directory and streams FFmpeg output to the container logs.
func Encode(ctx context.Context, config Config) error {
	args, err := BuildFFmpegArgs(config)
	if err != nil {
		return err
	}
	info, err := os.Stat(config.Source)
	if err != nil {
		return fmt.Errorf("read SOURCE %q: %w", config.Source, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("SOURCE %q must be a regular file", config.Source)
	}
	if err := os.MkdirAll(config.Output, 0o755); err != nil {
		return fmt.Errorf("create OUTPUT %q: %w", config.Output, err)
	}
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg failed for %dp: %w", config.Resolution, err)
	}
	return nil
}
