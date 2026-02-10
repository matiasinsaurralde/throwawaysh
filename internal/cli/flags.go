package cli

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/matiasinsaurralde/throwawaysh/pkg/server"
)

type ParseResult struct {
	Config              server.Config
	ShowVersion         bool
	CredentialsProvided bool
}

func Parse(args []string) (ParseResult, error) {
	fs := flag.NewFlagSet("throwawaysh", flag.ContinueOnError)

	listenAddr := fs.String("listen-addr", "", "SSH listen address (for example, :2222)")
	hostKeyPath := fs.String("host-key-path", "", "Path to the SSH host private key")
	username := fs.String("username", "", "Username required for password auth")
	password := fs.String("password", "", "Password required for password auth")
	allowPasswordless := fs.Bool("allow-passwordless", false, "Allow auth-less login for any username")
	logLevel := fs.String("log-level", "", "Log level: debug|info|warn|error")
	logFormat := fs.String("log-format", "", "Log format: text|json")
	version := fs.Bool("version", false, "Print version and exit")

	if err := fs.Parse(args); err != nil {
		return ParseResult{}, err
	}

	visited := map[string]bool{}
	fs.Visit(func(f *flag.Flag) {
		visited[f.Name] = true
	})

	allowPasswordlessValue, err := resolveBool(
		visited["allow-passwordless"],
		*allowPasswordless,
		"SSH_ALLOW_PASSWORDLESS",
		false,
	)
	if err != nil {
		return ParseResult{}, err
	}

	cfg := server.Config{
		ListenAddr:        resolveString(visited["listen-addr"], *listenAddr, "SSH_ADDR", server.DefaultListenAddr),
		HostKeyPath:       resolveString(visited["host-key-path"], *hostKeyPath, "SSH_HOST_KEY_PATH", server.DefaultHostKeyPath),
		Username:          resolveString(visited["username"], *username, "SSH_USERNAME", server.DefaultUsername),
		Password:          resolveString(visited["password"], *password, "SSH_PASSWORD", server.DefaultPassword),
		AllowPasswordless: allowPasswordlessValue,
		LogLevel:          strings.ToLower(resolveString(visited["log-level"], *logLevel, "SSH_LOG_LEVEL", server.DefaultLogLevel)),
		LogFormat:         strings.ToLower(resolveString(visited["log-format"], *logFormat, "SSH_LOG_FORMAT", server.DefaultLogFormat)),
	}

	if err := cfg.Validate(); err != nil {
		return ParseResult{}, err
	}

	credentialsProvided := visited["username"] || visited["password"] || os.Getenv("SSH_USERNAME") != "" || os.Getenv("SSH_PASSWORD") != ""

	return ParseResult{
		Config:              cfg,
		ShowVersion:         *version,
		CredentialsProvided: credentialsProvided,
	}, nil
}

func resolveString(flagSet bool, flagValue, envKey, fallback string) string {
	if flagSet {
		return flagValue
	}
	if value := os.Getenv(envKey); value != "" {
		return value
	}
	return fallback
}

func resolveBool(flagSet bool, flagValue bool, envKey string, fallback bool) (bool, error) {
	if flagSet {
		return flagValue, nil
	}
	value := os.Getenv(envKey)
	if value == "" {
		return fallback, nil
	}

	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("invalid boolean value for %s: %q", envKey, value)
	}
	return parsed, nil
}
