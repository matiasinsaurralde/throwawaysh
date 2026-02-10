package server

import (
	"errors"
	"log/slog"
	"time"

	"golang.org/x/crypto/ssh"
)

type exitStatus struct {
	Status uint32
}

func handleSession(
	cfg Config,
	logger *slog.Logger,
	channel ssh.Channel,
	requests <-chan *ssh.Request,
	remoteAddr, username string,
) {
	sessionReady := make(chan struct{})
	sessionErr := make(chan error, 1)

	defer func() {
		_ = channel.Close()
		logger.Info(
			"session closed",
			"event", "session_closed",
			"remote_addr", remoteAddr,
			"username", username,
		)
	}()

	go func() {
		started := false
		for req := range requests {
			switch req.Type {
			case "shell", "exec":
				_ = req.Reply(true, nil)
				if !started {
					started = true
					close(sessionReady)
				}
			case "env":
				_ = req.Reply(true, nil)
			case "pty-req", "window-change":
				// v1 uses plain console stream mode, so PTY semantics are not handled yet.
				_ = req.Reply(false, nil)
			default:
				_ = req.Reply(false, nil)
			}
		}

		if !started {
			sessionErr <- errors.New("session closed before shell/exec request")
		}
	}()

	select {
	case <-sessionReady:
	case reqErr := <-sessionErr:
		logger.Error(
			"session did not start",
			"event", "session_not_started",
			"remote_addr", remoteAddr,
			"username", username,
			"error", reqErr.Error(),
		)
		sendExitStatus(channel, 1)
		return
	case <-time.After(5 * time.Second):
		logger.Error(
			"session start request timeout",
			"event", "session_start_timeout",
			"remote_addr", remoteAddr,
			"username", username,
		)
		sendExitStatus(channel, 1)
		return
	}

	runErr := runKrunSession(cfg, logger, channel)
	if runErr != nil {
		logger.Error(
			"session execution failed",
			"event", "session_run_failed",
			"remote_addr", remoteAddr,
			"username", username,
			"error", runErr.Error(),
		)
		sendExitStatus(channel, 1)
		return
	}

	logger.Info(
		"session execution completed",
		"event", "session_completed",
		"remote_addr", remoteAddr,
		"username", username,
	)

	sendExitStatus(channel, 0)
}

func sendExitStatus(channel ssh.Channel, code uint32) {
	_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(exitStatus{Status: code}))
}
