package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"log/slog"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/matiasinsaurralde/throwawaysh/internal/cli"
	"github.com/matiasinsaurralde/throwawaysh/pkg/server"
	"github.com/matiasinsaurralde/throwawaysh/pkg/sshutil"
)

var version = "dev"

func main() {
	if server.IsKrunSessionChildProcess() {
		if err := server.RunKrunSessionChildFromEnv(); err != nil {
			log.Fatalf("failed to run krun session child: %v", err)
		}
		return
	}

	result, err := cli.Parse(os.Args[1:])
	if err != nil {
		log.Fatalf("failed to parse flags: %v", err)
	}

	if result.ShowVersion {
		_, _ = fmt.Fprintf(os.Stdout, "throwawaysh %s\n", version)
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	go func() {
		<-sigCh
		logger.Info("shutdown signal received", "event", "shutdown_signal_received")
		cancel()

		// If graceful shutdown gets stuck, a second signal forces exit.
		<-sigCh
		logger.Warn("forcing immediate exit", "event", "shutdown_forced")
		os.Exit(130)
	}()

	// Fallback for terminals/environments where Ctrl+C is delivered
	// as a raw byte instead of SIGINT.
	go watchStdinInterrupt(logger, cancel)

	runErr := srv.Run(ctx)
	if runErr != nil {
		logger.Error("server exited with error", "event", "server_runtime_error", "error", runErr.Error())
		os.Exit(1)
	}
}

func watchStdinInterrupt(logger *slog.Logger, cancel context.CancelFunc) {
	reader := bufio.NewReader(os.Stdin)
	for {
		b, err := reader.ReadByte()
		if err != nil {
			// stdin closed or unavailable; no fallback input possible.
			if err == io.EOF {
				return
			}
			return
		}
		if b == 3 {
			logger.Info("stdin interrupt received", "event", "shutdown_stdin_interrupt")
			cancel()
			return
		}
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
