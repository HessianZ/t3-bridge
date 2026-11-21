package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"os/signal"
	"syscall"
)

func runDesktopWorkspace(args []string, open func(context.Context, string) error) error {
	path, provider, err := configWindowOptions("gui", args)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serveConfigWorkspace(ctx, path, provider, open)
}
