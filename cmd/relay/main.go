package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/anchorshell/relay/pkg/relay"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := relay.Run(ctx, relay.DefaultOptions()); err != nil {
		slog.Error("relay stopped", "error", err)
		os.Exit(1)
	}
}
