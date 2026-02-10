package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"

	"golang.org/x/crypto/ssh"
)

type exitStatus struct {
	Status uint32
}

func main() {
	listenAddr := getenv("SSH_ADDR", ":2222")
	hostKeyPath := getenv("SSH_HOST_KEY_PATH", "server_key")
	username := getenv("SSH_USERNAME", "test")
	password := getenv("SSH_PASSWORD", "test")

	signer, err := loadOrCreateHostSigner(hostKeyPath)
	if err != nil {
		log.Fatalf("failed to load/create host key: %v", err)
	}

	config := &ssh.ServerConfig{
		PasswordCallback: func(conn ssh.ConnMetadata, provided []byte) (*ssh.Permissions, error) {
			if conn.User() == username && string(provided) == password {
				return nil, nil
			}
			return nil, fmt.Errorf("invalid credentials")
		},
	}
	config.AddHostKey(signer)

	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatalf("failed to listen on %s: %v", listenAddr, err)
	}
	defer listener.Close()

	log.Printf("ssh hello server listening on %s", listenAddr)
	log.Printf("test credentials: %s / %s", username, password)

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("failed to accept connection: %v", err)
			continue
		}
		go handleConn(conn, config)
	}
}

func handleConn(conn net.Conn, config *ssh.ServerConfig) {
	defer conn.Close()

	_, chans, reqs, err := ssh.NewServerConn(conn, config)
	if err != nil {
		log.Printf("handshake failed: %v", err)
		return
	}
	go ssh.DiscardRequests(reqs)

	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "only session channels are supported")
			continue
		}

		channel, requests, err := newChannel.Accept()
		if err != nil {
			log.Printf("failed to accept channel: %v", err)
			continue
		}

		go handleSession(channel, requests)
	}
}

func handleSession(channel ssh.Channel, requests <-chan *ssh.Request) {
	defer channel.Close()

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
		log.Printf("failed to write response: %v", err)
		return
	}

	_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(exitStatus{Status: 0}))
}

func loadOrCreateHostSigner(path string) (ssh.Signer, error) {
	keyData, err := os.ReadFile(path)
	if err == nil {
		return ssh.ParsePrivateKey(keyData)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}

	der := x509.MarshalPKCS1PrivateKey(privateKey)
	block := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}
	pemData := pem.EncodeToMemory(block)

	if err := os.WriteFile(path, pemData, 0o600); err != nil {
		return nil, err
	}

	return ssh.ParsePrivateKey(pemData)
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
