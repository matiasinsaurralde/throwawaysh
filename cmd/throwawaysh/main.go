package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/matiasinsaurralde/throwawaysh/internal/cli"
	"github.com/matiasinsaurralde/throwawaysh/pkg/server"
	"github.com/matiasinsaurralde/throwawaysh/pkg/sshutil"
)

var version = "dev"

func main() {
	result, err := cli.Parse(os.Args[1:])
	if err != nil {
		log.Fatalf("failed to parse flags: %v", err)
	}

	if result.ShowVersion {
		fmt.Printf("throwawaysh %s\n", version)
		return
	}

	logger, err := newLogger(result.Config.LogLevel, result.Config.LogFormat)
	if err != nil {
		log.Fatalf("failed to configure logger: %v", err)
	}

	if result.Config.AllowPasswordless && result.CredentialsProvided {
		logger.Warn(
			"passwordless mode enabled; username/password values are ignored",
			"event", "auth_options_ignored",
		)
	}

	signer, err := sshutil.LoadOrCreateHostSigner(result.Config.HostKeyPath)
	if err != nil {
		logger.Error("failed to load or create host key", "event", "hostkey_error", "error", err.Error())
		os.Exit(1)
	}

	srv, err := server.New(result.Config, signer, logger)
	if err != nil {
		logger.Error("failed to create server", "event", "server_init_failed", "error", err.Error())
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := srv.Run(ctx); err != nil {
		logger.Error("server exited with error", "event", "server_runtime_error", "error", err.Error())
		os.Exit(1)
	}
}

func newLogger(levelValue, format string) (*slog.Logger, error) {
	level := parseLevel(levelValue)
	options := &slog.HandlerOptions{
		Level: level,
	}

	switch format {
	case "json":
		return slog.New(slog.NewJSONHandler(os.Stdout, options)), nil
	case "text":
		return slog.New(slog.NewTextHandler(os.Stdout, options)), nil
	default:
		return nil, fmt.Errorf("unsupported log format: %s", format)
	}
}

func parseLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
