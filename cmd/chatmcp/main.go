package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/buenemann/chatmcp/internal/config"
	"github.com/buenemann/chatmcp/internal/notifier"
	"github.com/buenemann/chatmcp/internal/server"
	"github.com/buenemann/chatmcp/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "chatmcp:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open db at %s: %w", cfg.DBPath, err)
	}
	defer db.Close()
	fmt.Fprintf(os.Stderr, "chatmcp: db ready at %s\n", db.Path())

	srv := server.New(db)

	if cfg.AgentName != "" {
		if err := srv.PreClaim(ctx, cfg.AgentName, "unknown"); err != nil {
			fmt.Fprintf(os.Stderr, "chatmcp: CHATMCP_AGENT_NAME pre-claim failed (%v); proceeding unclaimed\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "chatmcp: pre-claimed %q\n", cfg.AgentName)
		}
	}

	// Run the notifier as a goroutine; it polls the shared DB for changes
	// committed by other chatmcp processes and pushes ResourceUpdated to our
	// subscribed clients. It exits when ctx is canceled.
	notifierDone := make(chan error, 1)
	go func() {
		notifierDone <- notifier.New(srv, cfg.PollInterval).Run(ctx)
	}()

	if err := srv.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	// Wait for notifier to wind down (best-effort; ctx is canceled).
	<-notifierDone
	return nil
}
