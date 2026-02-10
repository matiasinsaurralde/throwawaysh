//go:build linux

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/matiasinsaurralde/throwawaysh/pkg/agentproto"
	"golang.org/x/sys/unix"
)

func main() {
	port := flag.Int("port", 4000, "vsock port to listen on")
	flag.Parse()

	if err := run(*port); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "guest-agent error: %v\n", err)
		os.Exit(1)
	}
}

func run(port int) error {
	logf("starting guest-agent on vsock port %d", port)
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		return fmt.Errorf("create vsock socket: %w", err)
	}

	if err := connectVsockWithRetry(fd, uint32(port), 10*time.Second); err != nil {
		return fmt.Errorf("connect vsock host: %w", err)
	}
	logf("connected to host over vsock")
	conn := os.NewFile(uintptr(fd), "vsock-conn")
	defer func() {
		_ = conn.Close()
	}()

	frameType, payload, err := agentproto.ReadFrame(conn)
	if err != nil {
		return fmt.Errorf("read start frame: %w", err)
	}
	if frameType != agentproto.FrameStart {
		return fmt.Errorf("unexpected first frame type %d", frameType)
	}
	logf("received start frame (%d bytes)", len(payload))

	var start agentproto.StartPayload
	if err := agentproto.DecodeJSON(payload, &start); err != nil {
		return err
	}

	return runSession(conn, start)
}

func connectVsockWithRetry(fd int, port uint32, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		err := unix.Connect(fd, &unix.SockaddrVM{
			CID:  unix.VMADDR_CID_HOST,
			Port: port,
		})
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func runSession(conn *os.File, start agentproto.StartPayload) error {
	logf("starting shell term=%s rows=%d cols=%d command=%q", resolveTerm(start.Term), start.Rows, start.Columns, start.Command)
	var cmd *exec.Cmd
	if start.Command != "" {
		cmd = exec.Command("/bin/sh", "-c", start.Command)
	} else {
		cmd = exec.Command("/bin/sh", "-i")
	}
	cmd.Env = append(os.Environ(), "TERM="+resolveTerm(start.Term))

	ptmx, err := pty.Start(cmd)
	if err != nil {
		_ = writeErrorFrame(conn, fmt.Errorf("start shell: %w", err))
		return err
	}
	logf("shell started pid=%d", cmd.Process.Pid)
	defer func() {
		_ = ptmx.Close()
	}()

	if start.Rows > 0 && start.Columns > 0 {
		_ = pty.Setsize(ptmx, &pty.Winsize{
			Rows: uint16(start.Rows),
			Cols: uint16(start.Columns),
		})
	}

	readerErrCh := make(chan error, 1)
	writerErrCh := make(chan error, 1)
	waitErrCh := make(chan error, 1)
	controlClosedCh := make(chan struct{}, 1)

	go func() {
		waitErrCh <- cmd.Wait()
	}()

	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, readErr := ptmx.Read(buf)
			if n > 0 {
				if err := agentproto.WriteFrame(conn, agentproto.FrameOutput, buf[:n]); err != nil {
					writerErrCh <- err
					return
				}
			}
			if readErr != nil {
				if errors.Is(readErr, io.EOF) || errors.Is(readErr, syscall.EIO) {
					writerErrCh <- nil
					return
				}
				writerErrCh <- readErr
				return
			}
		}
	}()

	go func() {
		for {
			frameType, payload, err := agentproto.ReadFrame(conn)
			if err != nil {
				if errors.Is(err, io.EOF) {
					logf("control frame stream closed by host")
					controlClosedCh <- struct{}{}
					return
				}
				readerErrCh <- err
				return
			}

			switch frameType {
			case agentproto.FrameStdin:
				if len(payload) > 0 {
					_, _ = ptmx.Write(payload)
				}
			case agentproto.FrameResize:
				var resize agentproto.ResizePayload
				if err := agentproto.DecodeJSON(payload, &resize); err == nil {
					if resize.Rows > 0 && resize.Columns > 0 {
						_ = pty.Setsize(ptmx, &pty.Winsize{
							Rows: uint16(resize.Rows),
							Cols: uint16(resize.Columns),
						})
					}
				}
			case agentproto.FrameSignal:
				if cmd.Process != nil {
					_ = forwardSignal(cmd.Process, string(payload))
				}
			default:
				// Ignore unknown frame types for forward compatibility.
			}
		}
	}()

	controlClosed := false
	select {
	case waitErr := <-waitErrCh:
		exitCode := 0
		if waitErr != nil {
			var exitErr *exec.ExitError
			if errors.As(waitErr, &exitErr) {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = 1
			}
		}
		exitPayload, _ := agentproto.EncodeJSON(agentproto.ExitPayload{Code: exitCode})
		_ = agentproto.WriteFrame(conn, agentproto.FrameExit, exitPayload)
		logf("shell exited code=%d", exitCode)
		return nil
	case err := <-readerErrCh:
		_ = writeErrorFrame(conn, fmt.Errorf("read control frames: %w", err))
		logf("control frame reader failed: %v", err)
		return err
	case err := <-writerErrCh:
		if err == nil {
			logf("pty output stream ended")
			return nil
		}
		_ = writeErrorFrame(conn, fmt.Errorf("write output frame: %w", err))
		logf("pty output writer failed: %v", err)
		return err
	case <-controlClosedCh:
		controlClosed = true
	}

	// If control stream is closed, continue running until shell exits or PTY output errors.
	if controlClosed {
		for {
			select {
			case waitErr := <-waitErrCh:
				exitCode := 0
				if waitErr != nil {
					var exitErr *exec.ExitError
					if errors.As(waitErr, &exitErr) {
						exitCode = exitErr.ExitCode()
					} else {
						exitCode = 1
					}
				}
				exitPayload, _ := agentproto.EncodeJSON(agentproto.ExitPayload{Code: exitCode})
				_ = agentproto.WriteFrame(conn, agentproto.FrameExit, exitPayload)
				logf("shell exited code=%d after control close", exitCode)
				return nil
			case err := <-writerErrCh:
				if err == nil {
					logf("pty output stream ended after control close")
					return nil
				}
				_ = writeErrorFrame(conn, fmt.Errorf("write output frame: %w", err))
				logf("pty output writer failed after control close: %v", err)
				return err
			}
		}
	}

	return nil
}

func writeErrorFrame(conn io.Writer, err error) error {
	if err == nil {
		return nil
	}
	return agentproto.WriteFrame(conn, agentproto.FrameError, []byte(err.Error()))
}

func resolveTerm(term string) string {
	if term == "" {
		return "xterm-256color"
	}
	return term
}

func forwardSignal(process *os.Process, signalName string) error {
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

func logf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, "[guest-agent] "+format+"\n", args...)
}
