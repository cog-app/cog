package ssh

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
)

// LoadOrGenerateHostKey loads an SSH host key from path, or generates a new
// ed25519 key if the file does not exist. If path is empty, defaults to
// {dataDir}/ssh_host_ed25519_key.
func LoadOrGenerateHostKey(path, dataDir string) (ssh.Signer, error) {
	if path == "" {
		path = filepath.Join(dataDir, "ssh_host_ed25519_key")
	}

	data, err := os.ReadFile(path)
	if err == nil {
		return ssh.ParsePrivateKey(data)
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read host key: %w", err)
	}

	// Generate new ed25519 key.
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate host key: %w", err)
	}

	pemBlock, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return nil, fmt.Errorf("marshal host key: %w", err)
	}

	pemData := pem.EncodeToMemory(pemBlock)

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("create host key dir: %w", err)
	}
	if err := os.WriteFile(path, pemData, 0600); err != nil {
		return nil, fmt.Errorf("write host key: %w", err)
	}

	return ssh.ParsePrivateKey(pemData)
}
