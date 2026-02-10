package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"

	"golang.org/x/crypto/ssh"
)

type Server struct {
	cfg       Config
	logger    *slog.Logger
	sshConfig *ssh.ServerConfig
}

func New(cfg Config, signer ssh.Signer, logger *slog.Logger) (*Server, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	server := &Server{
		cfg:    cfg,
		logger: logger,
	}
	server.sshConfig = server.buildSSHConfig(signer)
	return server, nil
}

func (s *Server) Run(ctx context.Context) error {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", s.cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.cfg.ListenAddr, err)
	}
	defer func() {
		_ = listener.Close()
	}()

	s.logger.InfoContext(
		ctx,
		"service started",
		"event", "service_started",
		"listen_addr", s.cfg.ListenAddr,
		"auth_mode", s.authMode(),
	)

	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	for {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			if ctx.Err() != nil {
				s.logger.InfoContext(ctx, "service stopped", "event", "service_stopped")
				return nil
			}
			s.logger.ErrorContext(
				ctx,
				"failed accepting connection",
				"event", "listener_accept_failed",
				"error", acceptErr.Error(),
			)
			continue
		}

		s.logger.InfoContext(
			ctx,
			"connection accepted",
			"event", "connection_accepted",
			"remote_addr", conn.RemoteAddr().String(),
		)

		go s.handleConn(conn)
	}
}

func (s *Server) buildSSHConfig(signer ssh.Signer) *ssh.ServerConfig {
	cfg := &ssh.ServerConfig{
		NoClientAuth: s.cfg.AllowPasswordless,
	}

	if !s.cfg.AllowPasswordless {
		cfg.PasswordCallback = func(conn ssh.ConnMetadata, provided []byte) (*ssh.Permissions, error) {
			username := conn.User()
			remoteAddr := conn.RemoteAddr().String()
			if username == s.cfg.Username && string(provided) == s.cfg.Password {
				s.logger.Info(
					"authentication successful",
					"event", "auth_success",
					"remote_addr", remoteAddr,
					"username", username,
					"auth_mode", "password",
				)
				return &ssh.Permissions{}, nil
			}

			s.logger.Warn(
				"authentication failed",
				"event", "auth_failed",
				"remote_addr", remoteAddr,
				"username", username,
				"auth_mode", "password",
			)
			return nil, errors.New("invalid credentials")
		}
	}

	cfg.AddHostKey(signer)
	return cfg
}

func (s *Server) handleConn(conn net.Conn) {
	defer func() {
		_ = conn.Close()
	}()

	serverConn, chans, reqs, err := ssh.NewServerConn(conn, s.sshConfig)
	if err != nil {
		s.logger.Warn(
			"ssh handshake failed",
			"event", "handshake_failed",
			"remote_addr", conn.RemoteAddr().String(),
			"auth_mode", s.authMode(),
			"error", err.Error(),
		)
		return
	}
	defer func() {
		_ = serverConn.Close()
	}()

	if s.cfg.AllowPasswordless {
		s.logger.Info(
			"authentication successful",
			"event", "auth_success",
			"remote_addr", serverConn.RemoteAddr().String(),
			"username", serverConn.User(),
			"auth_mode", "passwordless",
		)
	}

	go ssh.DiscardRequests(reqs)

	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			s.logger.Warn(
				"unsupported channel rejected",
				"event", "channel_rejected",
				"remote_addr", serverConn.RemoteAddr().String(),
				"username", serverConn.User(),
				"channel_type", newChannel.ChannelType(),
			)
			_ = newChannel.Reject(ssh.UnknownChannelType, "only session channels are supported")
			continue
		}

		channel, requests, acceptErr := newChannel.Accept()
		if acceptErr != nil {
			s.logger.Error(
				"failed accepting channel",
				"event", "channel_accept_failed",
				"remote_addr", serverConn.RemoteAddr().String(),
				"username", serverConn.User(),
				"error", acceptErr.Error(),
			)
			continue
		}

		go handleSession(s.logger, channel, requests, serverConn.RemoteAddr().String(), serverConn.User())
	}
}

func (s *Server) authMode() string {
	if s.cfg.AllowPasswordless {
		return "passwordless"
	}
	return "password"
}
