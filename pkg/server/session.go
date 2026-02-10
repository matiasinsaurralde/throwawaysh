package server

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/crypto/ssh"
)

type exitStatus struct {
	Status uint32
}

type ptyRequest struct {
	Term    string
	Columns uint32
	Rows    uint32
	PixelsX uint32
	PixelsY uint32
	Modes   string
}

type windowChangeRequest struct {
	Columns uint32
	Rows    uint32
	PixelsX uint32
	PixelsY uint32
}

type signalRequest struct {
	Signal string
}

type sessionControlEvent struct {
	ResizeRows    uint32
	ResizeColumns uint32
	Signal        string
}

type sessionStartRequest struct {
	hasPTY        bool
	pty           *ptyRequest
	terminalModes ssh.TerminalModes
	execCommand   string
}

func handleSession(
	cfg Config,
	logger *slog.Logger,
	channel ssh.Channel,
	requests <-chan *ssh.Request,
	remoteAddr, username string,
) {
	sessionReady := make(chan sessionStartRequest, 1)
	sessionErr := make(chan error, 1)
	sessionControl := make(chan sessionControlEvent, 16)

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
		defer close(sessionControl)

		started := false
		hasPTY := false
		var ptyReq *ptyRequest
		terminalModes := ssh.TerminalModes{}
		for req := range requests {
			switch req.Type {
			case "shell":
				_ = req.Reply(true, nil)
				if !started {
					started = true
					sessionReady <- sessionStartRequest{
						hasPTY:        hasPTY,
						pty:           ptyReq,
						terminalModes: terminalModes,
					}
				}
			case "exec":
				_ = req.Reply(true, nil)
				if !started {
					var payload struct {
						Command string
					}
					if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
						sessionErr <- fmt.Errorf("invalid exec payload: %w", err)
						return
					}
					started = true
					sessionReady <- sessionStartRequest{
						hasPTY:        hasPTY,
						pty:           ptyReq,
						terminalModes: terminalModes,
						execCommand:   payload.Command,
					}
				}
			case "env":
				_ = req.Reply(true, nil)
			case "pty-req":
				var payload ptyRequest
				if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
					_ = req.Reply(false, nil)
					sessionErr <- fmt.Errorf("invalid pty-req payload: %w", err)
					return
				}
				parsedModes, err := parseTerminalModes(payload.Modes)
				if err != nil {
					_ = req.Reply(false, nil)
					sessionErr <- fmt.Errorf("invalid pty mode payload: %w", err)
					return
				}
				hasPTY = true
				ptyReq = &payload
				terminalModes = parsedModes
				_ = req.Reply(true, nil)
			case "window-change":
				var payload windowChangeRequest
				if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
					_ = req.Reply(false, nil)
					continue
				}
				if started {
					sessionControl <- sessionControlEvent{
						ResizeRows:    payload.Rows,
						ResizeColumns: payload.Columns,
					}
				}
				_ = req.Reply(true, nil)
			case "signal":
				var payload signalRequest
				if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
					_ = req.Reply(false, nil)
					continue
				}
				if started {
					sessionControl <- sessionControlEvent{
						Signal: payload.Signal,
					}
				}
				_ = req.Reply(true, nil)
			default:
				_ = req.Reply(false, nil)
			}
		}

		if !started {
			sessionErr <- errors.New("session closed before shell/exec request")
		}
	}()

	var startReq sessionStartRequest
	select {
	case startReq = <-sessionReady:
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

	runErr := runKrunSession(cfg, logger, channel, startReq, sessionControl)
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

func parseTerminalModes(encoded string) (ssh.TerminalModes, error) {
	modes := ssh.TerminalModes{}
	payload := []byte(encoded)

	for index := 0; index < len(payload); {
		opcode := payload[index]
		index++
		if opcode == 0 {
			return modes, nil
		}
		if len(payload)-index < 4 {
			return nil, fmt.Errorf("truncated value for opcode %d", opcode)
		}

		value := binary.BigEndian.Uint32(payload[index : index+4])
		index += 4
		modes[opcode] = value
	}

	return nil, errors.New("terminal modes missing end opcode")
}
