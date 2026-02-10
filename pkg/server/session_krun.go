package server

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"

	"github.com/creack/pty"
	"github.com/mishushakov/libkrun-go/krun"
	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
)

const (
	krunChildModeEnv        = "THROWAWAYSH_KRUN_CHILD"
	krunChildRootFSEnv      = "THROWAWAYSH_KRUN_ROOTFS"
	krunChildInteractiveEnv = "THROWAWAYSH_KRUN_INTERACTIVE"
	krunChildExecCommandEnv = "THROWAWAYSH_KRUN_EXEC_COMMAND"
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
	return runKrunInCurrentProcess(rootFS, interactive, execCommand, os.Stdin, os.Stdout, os.Stderr)
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
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}

	ptmx, tty, err := pty.Open()
	if err != nil {
		return fmt.Errorf("open pty: %w", err)
	}
	defer func() {
		_ = ptmx.Close()
		_ = tty.Close()
	}()

	if startReq.pty != nil {
		_ = pty.Setsize(ptmx, &pty.Winsize{
			Rows: uint16(startReq.pty.Rows),
			Cols: uint16(startReq.pty.Columns),
		})
	}
	if err := configureHostPTY(tty, startReq.terminalModes); err != nil {
		logger.Warn(
			"failed to apply pty host terminal settings",
			"event", "session_pty_mode_apply_failed",
			"error", err.Error(),
		)
	}

	cmd := exec.Command(execPath)
	cmd.Env = append(
		os.Environ(),
		krunChildModeEnv+"=1",
		krunChildRootFSEnv+"="+cfg.RootFS,
		krunChildInteractiveEnv+"=1",
		krunChildExecCommandEnv+"="+startReq.execCommand,
		"TERM="+resolveTERM(startReq),
	)
	cmd.Stdin = tty
	cmd.Stdout = tty
	cmd.Stderr = tty

	logger.Info("starting krun vm for ssh pty session", "event", "session_vm_start")
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start vm child process: %w", err)
	}
	_ = tty.Close()

	doneForward := make(chan struct{})
	go func() {
		defer close(doneForward)
		for event := range sessionControl {
			if event.ResizeColumns > 0 && event.ResizeRows > 0 {
				_ = pty.Setsize(ptmx, &pty.Winsize{
					Rows: uint16(event.ResizeRows),
					Cols: uint16(event.ResizeColumns),
				})
			}
			if event.Signal != "" {
				_ = forwardSSHSignal(cmd.Process, event.Signal)
			}
		}
	}()

	copyErrors := make(chan error, 2)
	go pipeCopy(copyErrors, ptmx, channel)
	go pipeCopy(copyErrors, channel, ptmx)

	waitErr := cmd.Wait()
	_ = ptmx.Close()

	// Best-effort drain of copy goroutines after PTY close.
	for range 2 {
		select {
		case <-copyErrors:
		case <-doneForward:
		}
	}

	if waitErr != nil {
		return fmt.Errorf("run vm child process: %w", waitErr)
	}

	logger.Info("krun vm pty session ended", "event", "session_vm_end")
	return nil
}

func runKrunInCurrentProcess(rootFS string, interactive bool, execCommand string, stdin, stdout, stderr *os.File) error {
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

	execCfg := krun.ExecConfig{
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

	if err := ctx.SetExec(execCfg); err != nil {
		return fmt.Errorf("set exec: %w", err)
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

func pipeCopy(errCh chan<- error, dst io.Writer, src io.Reader) {
	_, err := io.Copy(dst, src)
	if err != nil && !errors.Is(err, io.EOF) {
		errCh <- err
		return
	}
	errCh <- nil
}

func forwardSSHSignal(process *os.Process, signalName string) error {
	if process == nil {
		return nil
	}

	trimmed := strings.ToUpper(strings.TrimPrefix(signalName, "SIG"))
	signalMap := map[string]syscall.Signal{
		"ABRT": syscall.SIGABRT,
		"ALRM": syscall.SIGALRM,
		"FPE":  syscall.SIGFPE,
		"HUP":  syscall.SIGHUP,
		"ILL":  syscall.SIGILL,
		"INT":  syscall.SIGINT,
		"KILL": syscall.SIGKILL,
		"PIPE": syscall.SIGPIPE,
		"QUIT": syscall.SIGQUIT,
		"SEGV": syscall.SIGSEGV,
		"TERM": syscall.SIGTERM,
		"USR1": syscall.SIGUSR1,
		"USR2": syscall.SIGUSR2,
	}
	sig, ok := signalMap[trimmed]
	if !ok {
		return nil
	}

	return process.Signal(sig)
}

func configureHostPTY(tty *os.File, modes ssh.TerminalModes) error {
	termios, setRequest, err := getTTYTermios(tty.Fd())
	if err != nil {
		return err
	}

	// Keep this host PTY as a transport layer and avoid double line discipline
	// with the guest shell TTY.
	makeTermiosRaw(termios)
	applyControlChars(termios, modes)
	applyLineSpeed(termios, modes)

	return unix.IoctlSetTermios(int(tty.Fd()), setRequest, termios)
}

func getTTYTermios(fd uintptr) (*unix.Termios, uint, error) {
	getRequest, setRequest := termiosGetSetRequests()
	termios, err := unix.IoctlGetTermios(int(fd), getRequest)
	if err != nil {
		return nil, 0, fmt.Errorf("get tty termios: %w", err)
	}
	return termios, setRequest, nil
}

func makeTermiosRaw(termios *unix.Termios) {
	termios.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	termios.Oflag &^= unix.OPOST
	termios.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	termios.Cflag &^= unix.CSIZE | unix.PARENB
	termios.Cflag |= unix.CS8
	termios.Cc[unix.VMIN] = 1
	termios.Cc[unix.VTIME] = 0
}

func applyControlChars(termios *unix.Termios, modes ssh.TerminalModes) {
	controlCharMappings := map[uint8]uint8{
		ssh.VINTR:    unix.VINTR,
		ssh.VQUIT:    unix.VQUIT,
		ssh.VERASE:   unix.VERASE,
		ssh.VKILL:    unix.VKILL,
		ssh.VEOF:     unix.VEOF,
		ssh.VEOL:     unix.VEOL,
		ssh.VEOL2:    unix.VEOL2,
		ssh.VSTART:   unix.VSTART,
		ssh.VSTOP:    unix.VSTOP,
		ssh.VSUSP:    unix.VSUSP,
		ssh.VREPRINT: unix.VREPRINT,
		ssh.VWERASE:  unix.VWERASE,
		ssh.VLNEXT:   unix.VLNEXT,
		ssh.VDISCARD: unix.VDISCARD,
	}

	for sshOpcode, ccIndex := range controlCharMappings {
		value, exists := modes[sshOpcode]
		if !exists {
			continue
		}
		termios.Cc[ccIndex] = uint8(value)
	}
}
