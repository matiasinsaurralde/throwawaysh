package server

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/matiasinsaurralde/throwawaysh/pkg/agentproto"
	"github.com/mishushakov/libkrun-go/krun"
	"golang.org/x/crypto/ssh"
)

const (
	krunChildModeEnv        = "THROWAWAYSH_KRUN_CHILD"
	krunChildRootFSEnv      = "THROWAWAYSH_KRUN_ROOTFS"
	krunChildInteractiveEnv = "THROWAWAYSH_KRUN_INTERACTIVE"
	krunChildExecCommandEnv = "THROWAWAYSH_KRUN_EXEC_COMMAND"
	krunChildUseGuestAgent  = "THROWAWAYSH_KRUN_USE_GUEST_AGENT"
	krunChildAgentPortEnv   = "THROWAWAYSH_KRUN_AGENT_PORT"
	krunChildAgentSockEnv   = "THROWAWAYSH_KRUN_AGENT_SOCKET_PATH"
	krunChildAgentExecEnv   = "THROWAWAYSH_KRUN_AGENT_EXEC_PATH"
	krunChildAgentDebugEnv  = "THROWAWAYSH_KRUN_AGENT_DEBUG"

	defaultGuestAgentExecPath = "/usr/local/bin/throwawaysh-guest-agent"
)

var (
	krunLogInitOnce sync.Once
	krunLogInitErr  error
)

func initKrunLogLevel() error {
	krunLogInitOnce.Do(func() {
		krunLogInitErr = krun.SetLogLevel(krun.LogLevelInfo)
	})
	if krunLogInitErr != nil {
		return fmt.Errorf("set krun log level: %w", krunLogInitErr)
	}
	return nil
}

func IsKrunSessionChildProcess() bool {
	return os.Getenv(krunChildModeEnv) == "1"
}

func RunKrunSessionChildFromEnv() error {
	rootFS := os.Getenv(krunChildRootFSEnv)
	if rootFS == "" {
		return fmt.Errorf("%s is required in child mode", krunChildRootFSEnv)
	}
	interactive := os.Getenv(krunChildInteractiveEnv) == "1"
	execCommand := os.Getenv(krunChildExecCommandEnv)
	useGuestAgent := os.Getenv(krunChildUseGuestAgent) == "1"
	guestAgentPort := uint32(4000)
	if portValue := os.Getenv(krunChildAgentPortEnv); portValue != "" {
		parsedPort, err := strconv.ParseUint(portValue, 10, 32)
		if err != nil {
			return fmt.Errorf("parse %s: %w", krunChildAgentPortEnv, err)
		}
		guestAgentPort = uint32(parsedPort)
	}
	guestAgentSocketPath := os.Getenv(krunChildAgentSockEnv)
	guestAgentExecPath := os.Getenv(krunChildAgentExecEnv)
	if guestAgentExecPath == "" {
		guestAgentExecPath = defaultGuestAgentExecPath
	}

	return runKrunInCurrentProcess(
		rootFS,
		interactive,
		execCommand,
		useGuestAgent,
		guestAgentPort,
		guestAgentSocketPath,
		guestAgentExecPath,
		os.Stdin,
		os.Stdout,
		os.Stderr,
	)
}

func runKrunSession(
	cfg Config,
	logger *slog.Logger,
	channel ssh.Channel,
	startReq sessionStartRequest,
	sessionControl <-chan sessionControlEvent,
) error {
	if startReq.hasPTY {
		return runKrunPTYSession(cfg, logger, channel, startReq, sessionControl)
	}

	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}

	cmd := exec.Command(execPath)
	cmd.Env = append(
		os.Environ(),
		krunChildModeEnv+"=1",
		krunChildRootFSEnv+"="+cfg.RootFS,
		krunChildInteractiveEnv+"="+boolToEnvValue(startReq.hasPTY),
		krunChildExecCommandEnv+"="+startReq.execCommand,
		"TERM="+resolveTERM(startReq),
	)
	cmd.Stdin = channel
	cmd.Stdout = channel
	cmd.Stderr = channel.Stderr()

	logger.Info("starting krun vm for ssh session", "event", "session_vm_start")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run vm child process: %w", err)
	}

	logger.Info("krun vm session ended", "event", "session_vm_end")
	return nil
}

func runKrunPTYSession(
	cfg Config,
	logger *slog.Logger,
	channel ssh.Channel,
	startReq sessionStartRequest,
	sessionControl <-chan sessionControlEvent,
) error {
	guestAgentPath := filepath.Join(cfg.RootFS, strings.TrimPrefix(defaultGuestAgentExecPath, "/"))
	if _, err := os.Stat(guestAgentPath); err != nil {
		return fmt.Errorf("guest agent not found in rootfs at %s: %w", guestAgentPath, err)
	}

	socketPath := filepath.Join(
		os.TempDir(),
		fmt.Sprintf("throwawaysh-agent-%d.sock", time.Now().UnixNano()),
	)
	guestAgentPort := chooseSessionAgentPort()
	_ = os.Remove(socketPath)
	defer func() {
		_ = os.Remove(socketPath)
	}()

	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}

	cmd := exec.Command(execPath)
	cmd.Env = append(
		os.Environ(),
		krunChildModeEnv+"=1",
		krunChildRootFSEnv+"="+cfg.RootFS,
		krunChildInteractiveEnv+"=1",
		krunChildExecCommandEnv+"="+startReq.execCommand,
		krunChildUseGuestAgent+"=1",
		krunChildAgentPortEnv+"="+strconv.FormatUint(uint64(guestAgentPort), 10),
		krunChildAgentSockEnv+"="+socketPath,
		krunChildAgentExecEnv+"="+defaultGuestAgentExecPath,
		krunChildAgentDebugEnv+"=1",
		"TERM="+resolveTERM(startReq),
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr

	logger.Info(
		"starting krun vm guest-agent pty session",
		"event", "session_vm_start",
		"agent_socket_path", socketPath,
		"agent_port", guestAgentPort,
		"agent_exec_path", defaultGuestAgentExecPath,
	)

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen guest-agent socket: %w", err)
	}
	defer func() {
		_ = listener.Close()
	}()
	logger.Debug("guest-agent host socket listener ready", "event", "guest_agent_host_socket_ready", "path", socketPath)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start vm child process: %w", err)
	}
	logger.Debug("vm child process started", "event", "session_vm_child_started", "pid", cmd.Process.Pid)

	agentConn, err := acceptAgentSocket(listener, 20*time.Second, logger)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("accept guest agent socket: %w", err)
	}
	defer func() {
		_ = agentConn.Close()
	}()
	logger.Info("guest-agent socket connected", "event", "guest_agent_connected", "agent_socket_path", socketPath)

	startPayload := agentproto.StartPayload{
		Term:    resolveTERM(startReq),
		Rows:    valueOrDefault(startReq.pty, func(p *ptyRequest) uint32 { return p.Rows }, 24),
		Columns: valueOrDefault(startReq.pty, func(p *ptyRequest) uint32 { return p.Columns }, 80),
		Command: startReq.execCommand,
	}
	startPayloadBytes, err := agentproto.EncodeJSON(startPayload)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("encode start payload: %w", err)
	}
	if err := agentproto.WriteFrame(agentConn, agentproto.FrameStart, startPayloadBytes); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("send start payload: %w", err)
	}
	logger.Debug("sent guest-agent start payload", "event", "guest_agent_start_sent")

	agentErrCh := make(chan error, 1)
	agentExitCodeCh := make(chan int, 1)
	go readGuestAgentFrames(logger, agentConn, channel, agentErrCh, agentExitCodeCh)

	go forwardChannelInputToAgent(logger, agentConn, channel)
	go forwardSessionControlToAgent(logger, agentConn, sessionControl)

	cmdWaitCh := make(chan error, 1)
	go func() {
		cmdWaitCh <- cmd.Wait()
	}()

	select {
	case err := <-agentErrCh:
		_ = cmd.Process.Kill()
		_ = <-cmdWaitCh
		return fmt.Errorf("guest agent stream error: %w", err)
	case exitCode := <-agentExitCodeCh:
		waitErr := <-cmdWaitCh
		if waitErr != nil {
			return fmt.Errorf("run vm child process: %w", waitErr)
		}
		if exitCode != 0 {
			return fmt.Errorf("guest agent exited with code %d", exitCode)
		}
	case waitErr := <-cmdWaitCh:
		if waitErr != nil {
			return fmt.Errorf("run vm child process: %w", waitErr)
		}
		select {
		case err := <-agentErrCh:
			if err != nil && !errors.Is(err, io.EOF) {
				return fmt.Errorf("guest agent stream error after vm exit: %w", err)
			}
		case exitCode := <-agentExitCodeCh:
			if exitCode != 0 {
				return fmt.Errorf("guest agent exited with code %d", exitCode)
			}
		case <-time.After(2 * time.Second):
			return errors.New("guest agent did not send exit frame before timeout")
		}
	}

	logger.Info("krun vm guest-agent pty session ended", "event", "session_vm_end")
	return nil
}

func runKrunInCurrentProcess(
	rootFS string,
	interactive bool,
	execCommand string,
	useGuestAgent bool,
	guestAgentPort uint32,
	guestAgentSocketPath string,
	guestAgentExecPath string,
	stdin, stdout, stderr *os.File,
) error {
	if err := initKrunLogLevel(); err != nil {
		return err
	}

	ctx, err := krun.CreateContext()
	if err != nil {
		return fmt.Errorf("create krun context: %w", err)
	}
	defer func() {
		_ = ctx.Free()
	}()

	if err := ctx.SetVMConfig(krun.VMConfig{NumVCPUs: 2, RAMMiB: 512}); err != nil {
		return fmt.Errorf("set vm config: %w", err)
	}
	if err := ctx.SetRoot(rootFS); err != nil {
		return fmt.Errorf("set rootfs: %w", err)
	}

	env := []string{
		"PATH=/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin",
		"HOME=/root",
	}
	env = append(env, "TERM="+resolveTermEnv())

	var execCfg krun.ExecConfig
	if useGuestAgent {
		execCfg = krun.ExecConfig{
			Path: guestAgentExecPath,
			Args: []string{"--port", strconv.FormatUint(uint64(guestAgentPort), 10)},
			Env:  env,
		}
	} else {
		execCfg = krun.ExecConfig{
			Path: "/bin/sh",
			Env:  env,
		}
		switch {
		case execCommand != "":
			execCfg.Args = []string{"-c", execCommand}
		case interactive:
			execCfg.Args = []string{"-i"}
		default:
			// Non-PTY shell sessions (for example ssh -T) should avoid forcing -i.
			// Running interactive shell mode without a PTY can cause flaky command input.
			execCfg.Args = nil
		}
	}

	if err := ctx.SetExec(execCfg); err != nil {
		return fmt.Errorf("set exec: %w", err)
	}
	childDebugf(stderr, "exec configured path=%s args=%v use_guest_agent=%t", execCfg.Path, execCfg.Args, useGuestAgent)

	if useGuestAgent {
		if guestAgentSocketPath == "" {
			return errors.New("guest agent mode requires a host socket path")
		}
		childDebugf(stderr, "configuring guest-agent exec=%s port=%d socket=%s", guestAgentExecPath, guestAgentPort, guestAgentSocketPath)
		if err := ctx.AddVsockPort(krun.VsockPortConfig{
			Port:   guestAgentPort,
			Path:   guestAgentSocketPath,
			Listen: false,
		}); err != nil {
			return fmt.Errorf("add vsock port: %w", err)
		}
	}

	if err := ctx.AddVirtioConsoleDefault(krun.VirtioConsoleConfig{
		InputFD:  int(stdin.Fd()),
		OutputFD: int(stdout.Fd()),
		ErrFD:    int(stderr.Fd()),
	}); err != nil {
		return fmt.Errorf("add virtio console: %w", err)
	}

	startErr := ctx.StartEnter()
	if startErr != nil {
		return fmt.Errorf("start vm: %w", startErr)
	}
	childDebugf(stderr, "vm start enter completed")

	return nil
}

func boolToEnvValue(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

func resolveTERM(startReq sessionStartRequest) string {
	if startReq.pty == nil || startReq.pty.Term == "" {
		return resolveTermEnv()
	}
	return startReq.pty.Term
}

func resolveTermEnv() string {
	if term := os.Getenv("TERM"); term != "" {
		return term
	}
	return "xterm-256color"
}

func acceptAgentSocket(listener net.Listener, timeout time.Duration, logger *slog.Logger) (net.Conn, error) {
	type acceptResult struct {
		conn net.Conn
		err  error
	}
	resultCh := make(chan acceptResult, 1)
	go func() {
		conn, err := listener.Accept()
		resultCh <- acceptResult{conn: conn, err: err}
	}()

	select {
	case result := <-resultCh:
		if result.err != nil {
			return nil, result.err
		}
		return result.conn, nil
	case <-time.After(timeout):
		logger.Warn("timed out waiting for guest-agent connection", "event", "guest_agent_accept_timeout")
		return nil, errors.New("timed out waiting for guest-agent connection")
	}
}

func chooseSessionAgentPort() uint32 {
	// Keep to user-space dynamic range and vary by time to reduce parallel collisions.
	const minPort uint32 = 20000
	const rangeSize uint32 = 30000
	return minPort + uint32(time.Now().UnixNano()%int64(rangeSize))
}

func readGuestAgentFrames(
	logger *slog.Logger,
	conn net.Conn,
	channel ssh.Channel,
	errCh chan<- error,
	exitCodeCh chan<- int,
) {
	for {
		frameType, payload, err := agentproto.ReadFrame(conn)
		if err != nil {
			logger.Debug(
				"guest-agent frame read ended",
				"event", "guest_agent_frame_read_end",
				"error", err.Error(),
			)
			errCh <- err
			return
		}

		switch frameType {
		case agentproto.FrameOutput:
			if len(payload) > 0 {
				if _, writeErr := channel.Write(payload); writeErr != nil {
					errCh <- fmt.Errorf("write agent output to ssh channel: %w", writeErr)
					return
				}
			}
		case agentproto.FrameError:
			if len(payload) > 0 {
				logger.Warn(
					"guest-agent reported error frame",
					"event", "guest_agent_error_frame",
					"error", string(payload),
				)
				_, _ = channel.Stderr().Write(payload)
				_, _ = channel.Stderr().Write([]byte("\n"))
			}
		case agentproto.FrameExit:
			var exitPayload agentproto.ExitPayload
			if decodeErr := agentproto.DecodeJSON(payload, &exitPayload); decodeErr != nil {
				errCh <- decodeErr
				return
			}
			logger.Info("guest-agent exit frame received", "event", "guest_agent_exit_frame", "exit_code", exitPayload.Code)
			exitCodeCh <- exitPayload.Code
			return
		default:
			logger.Debug("unknown guest-agent frame ignored", "event", "guest_agent_unknown_frame", "type", frameType)
		}
	}
}

func forwardChannelInputToAgent(logger *slog.Logger, conn net.Conn, channel ssh.Channel) {
	type closeWriter interface {
		CloseWrite() error
	}

	buffer := make([]byte, 32*1024)
	for {
		readBytes, err := channel.Read(buffer)
		if readBytes > 0 {
			if writeErr := agentproto.WriteFrame(conn, agentproto.FrameStdin, buffer[:readBytes]); writeErr != nil {
				logger.Warn(
					"failed forwarding stdin frame to guest-agent",
					"event", "guest_agent_stdin_forward_failed",
					"error", writeErr.Error(),
				)
				return
			}
		}
		if err != nil {
			logger.Debug("ssh channel input closed", "event", "guest_agent_stdin_closed")
			if closer, ok := conn.(closeWriter); ok {
				_ = closer.CloseWrite()
			}
			return
		}
	}
}

func forwardSessionControlToAgent(logger *slog.Logger, conn net.Conn, sessionControl <-chan sessionControlEvent) {
	for event := range sessionControl {
		if event.ResizeRows > 0 && event.ResizeColumns > 0 {
			payload, err := agentproto.EncodeJSON(agentproto.ResizePayload{
				Rows:    event.ResizeRows,
				Columns: event.ResizeColumns,
			})
			if err == nil {
				logger.Debug(
					"forwarding resize to guest-agent",
					"event", "guest_agent_resize_forward",
					"rows", event.ResizeRows,
					"columns", event.ResizeColumns,
				)
				_ = agentproto.WriteFrame(conn, agentproto.FrameResize, payload)
			}
		}
		if event.Signal != "" {
			logger.Debug(
				"forwarding signal to guest-agent",
				"event", "guest_agent_signal_forward",
				"signal", event.Signal,
			)
			_ = agentproto.WriteFrame(conn, agentproto.FrameSignal, []byte(event.Signal))
		}
	}
}

func valueOrDefault[T any](value *ptyRequest, getter func(*ptyRequest) T, fallback T) T {
	if value == nil {
		return fallback
	}
	return getter(value)
}

func childDebugf(stderr io.Writer, format string, args ...any) {
	if os.Getenv(krunChildAgentDebugEnv) != "1" {
		return
	}
	_, _ = fmt.Fprintf(stderr, "[throwawaysh-child] "+format+"\n", args...)
}
