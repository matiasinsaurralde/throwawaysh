package sshutil

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"

	"golang.org/x/crypto/ssh"
)

func LoadOrCreateHostSigner(path string) (ssh.Signer, error) {
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
