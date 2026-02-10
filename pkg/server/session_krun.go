package server

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"

	"github.com/mishushakov/libkrun-go/krun"
	"golang.org/x/crypto/ssh"
)

func runKrunSession(cfg Config, logger *slog.Logger, channel ssh.Channel) error {
	if err := krun.SetLogLevel(krun.LogLevelInfo); err != nil {
		return fmt.Errorf("set krun log level: %w", err)
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
	if err := ctx.SetRoot(cfg.RootFS); err != nil {
		return fmt.Errorf("set rootfs: %w", err)
	}

	env := []string{
		"PATH=/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin",
		"HOME=/root",
		"TERM=xterm-256color",
	}
	if err := ctx.SetExec(krun.ExecConfig{Path: "/bin/sh", Args: []string{"-i"}, Env: env}); err != nil {
		return fmt.Errorf("set exec: %w", err)
	}

	vmInR, vmInW, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("create vm input pipe: %w", err)
	}
	defer func() {
		_ = vmInR.Close()
		_ = vmInW.Close()
	}()

	vmOutR, vmOutW, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("create vm output pipe: %w", err)
	}
	defer func() {
		_ = vmOutR.Close()
		_ = vmOutW.Close()
	}()

	vmErrR, vmErrW, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("create vm stderr pipe: %w", err)
	}
	defer func() {
		_ = vmErrR.Close()
		_ = vmErrW.Close()
	}()

	if err := ctx.AddVirtioConsoleDefault(krun.VirtioConsoleConfig{
		InputFD:  int(vmInR.Fd()),
		OutputFD: int(vmOutW.Fd()),
		ErrFD:    int(vmErrW.Fd()),
	}); err != nil {
		return fmt.Errorf("add virtio console: %w", err)
	}

	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		_, _ = io.Copy(vmInW, channel)
		_ = vmInW.Close()
	}()

	go func() {
		defer wg.Done()
		_, _ = io.Copy(channel, vmOutR)
	}()

	go func() {
		defer wg.Done()
		_, _ = io.Copy(channel.Stderr(), vmErrR)
	}()

	logger.Info("starting krun vm for ssh session", "event", "session_vm_start")
	startErr := ctx.StartEnter()
	if startErr != nil {
		return fmt.Errorf("start vm: %w", startErr)
	}

	wg.Wait()
	logger.Info("krun vm session ended", "event", "session_vm_end")
	return nil
}
