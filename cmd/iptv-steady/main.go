package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"iptv-steady/internal/app"
	"iptv-steady/internal/config"
)

const usage = `iptv-steady serves several Xtream accounts as one stable service.

Usage: iptv-steady [-config config.yaml] [-days 7] <command>

  serve    Start the proxy (default)
  sync     Download every catalog once and print a summary
  report   Summarize playback interruptions and recoveries
`

func main() {
	if err := run(); err != nil {
		slog.Error("stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "config.yaml", "configuration file")
	days := flag.Int("days", 7, "days covered by the report")
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	instance, err := app.New(cfg)
	if err != nil {
		return err
	}
	defer instance.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	command := "serve"
	if flag.NArg() > 0 {
		command = flag.Arg(0)
	}
	switch command {
	case "serve":
		return instance.Serve(ctx)
	case "sync":
		return instance.Sync(ctx, os.Stdout)
	case "report":
		return instance.Report(os.Stdout, *days)
	}
	flag.Usage()
	return errors.New("unknown command: " + command)
}
