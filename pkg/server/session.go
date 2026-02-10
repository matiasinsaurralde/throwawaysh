package server

import (
	"io"
	"log/slog"

	"golang.org/x/crypto/ssh"
)

type exitStatus struct {
	Status uint32
}

func handleSession(logger *slog.Logger, channel ssh.Channel, requests <-chan *ssh.Request, remoteAddr, username string) {
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
		for req := range requests {
			switch req.Type {
			case "shell", "exec", "pty-req", "env", "window-change":
				_ = req.Reply(true, nil)
			default:
				_ = req.Reply(false, nil)
			}
		}
	}()

	if _, err := io.WriteString(channel, "hello\n"); err != nil {
		logger.Error(
			"failed writing greeting",
			"event", "session_write_failed",
			"remote_addr", remoteAddr,
			"username", username,
			"error", err.Error(),
		)
		return
	}

	logger.Info(
		"greeting sent",
		"event", "session_greeting_sent",
		"remote_addr", remoteAddr,
		"username", username,
	)

	_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(exitStatus{Status: 0}))
}
