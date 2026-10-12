package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/shotforward/shotforward/daemon/internal/config"
	"github.com/shotforward/shotforward/daemon/internal/host"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildTime = "unknown"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.LookupEnv))
}

func run(args []string, stdout, stderr io.Writer, lookupEnv config.LookupEnv) int {
	flags := flag.NewFlagSet("shotforwardd", flag.ContinueOnError)
	flags.SetOutput(stderr)
	showVersion := flags.Bool("version", false, "print version information and exit")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "shotforwardd: unexpected arguments: %v\n", flags.Args())
		return 2
	}
	if *showVersion {
		fmt.Fprintf(stdout, "shotforwardd %s (commit %s, built %s)\n", version, commit, buildTime)
		return 0
	}

	cfg, err := config.Load(lookupEnv)
	if err != nil {
		fmt.Fprintf(stderr, "shotforwardd: %v\n", err)
		return 1
	}

	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := host.Run(ctx, cfg, logger, stdout); err != nil {
		logger.Error("ShotForward daemon exited with an error", "error", err)
		return 1
	}

	return 0
}
