//go:build linux

package server

import (
	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
)

func termiosGetSetRequests() (uint, uint) {
	return uint(unix.TCGETS), uint(unix.TCSETS)
}

func applyLineSpeed(termios *unix.Termios, modes ssh.TerminalModes) {
	if mode, ok := modes[ssh.TTY_OP_ISPEED]; ok && mode > 0 {
		termios.Ispeed = uint32(mode)
	}
	if mode, ok := modes[ssh.TTY_OP_OSPEED]; ok && mode > 0 {
		termios.Ospeed = uint32(mode)
	}
}
