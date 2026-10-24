package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/martupiru/MansiFlix/video-worker/internal/encoder"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		log.Printf("Video encoding failed: %v", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	config, err := encoder.ConfigFromEnv(os.Getenv)
	if err != nil {
		return err
	}
	log.Printf("Encoding %s at %dp into %s", config.Source, config.Resolution, config.Output)
	return encoder.Encode(ctx, config)
}
