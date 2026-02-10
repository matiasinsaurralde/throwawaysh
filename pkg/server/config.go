package server

import (
	"errors"
	"fmt"
	"slices"
)

const (
	DefaultListenAddr  = ":2222"
	DefaultHostKeyPath = "server_key"
	DefaultUsername    = "test"
	DefaultPassword    = "test"
	DefaultLogLevel    = "info"
	DefaultLogFormat   = "text"
)

type Config struct {
	ListenAddr        string
	HostKeyPath       string
	RootFS            string
	Username          string
	Password          string
	AllowPasswordless bool
	LogLevel          string
	LogFormat         string
}

func (c Config) Validate() error {
	if c.ListenAddr == "" {
		return errors.New("listen address is required")
	}
	if c.HostKeyPath == "" {
		return errors.New("host key path is required")
	}
	if c.RootFS == "" {
		return errors.New("rootfs is required")
	}
	if !c.AllowPasswordless {
		if c.Username == "" {
			return errors.New("username is required when passwordless mode is disabled")
		}
		if c.Password == "" {
			return errors.New("password is required when passwordless mode is disabled")
		}
	}

	validLevels := []string{"debug", "info", "warn", "error"}
	if !slices.Contains(validLevels, c.LogLevel) {
		return fmt.Errorf("invalid log level %q (expected: debug|info|warn|error)", c.LogLevel)
	}

	validFormats := []string{"text", "json"}
	if !slices.Contains(validFormats, c.LogFormat) {
		return fmt.Errorf("invalid log format %q (expected: text|json)", c.LogFormat)
	}

	return nil
}
