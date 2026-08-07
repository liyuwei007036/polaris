package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/liyuwei007036/polaris/internal/wire"
)

const (
	noisePrivateKeyFile         = "agent-noise.key"
	releaseSigningPublicKeyFile = "release-manifest.pub"
)

// LoadOrCreateKeypair loads the agent's persistent Curve25519 identity,
// generating one on first run. This is the WireGuard-style replacement for
// the old CSR/certificate flow: the master pins this key's public half once
// an operator approves it, and there is nothing to sign or issue.
func LoadOrCreateKeypair(dataDir string) (wire.Keypair, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return wire.Keypair{}, fmt.Errorf("create agent data directory: %w", err)
	}
	path := filepath.Join(dataDir, noisePrivateKeyFile)
	value, err := os.ReadFile(path)
	if err == nil {
		if len(value) != wire.KeySize {
			return wire.Keypair{}, errors.New("agent private key has unexpected length")
		}
		var private [wire.KeySize]byte
		copy(private[:], value)
		return wire.KeypairFromPrivate(private)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return wire.Keypair{}, fmt.Errorf("read agent private key: %w", err)
	}
	keypair, err := wire.GenerateKeypair()
	if err != nil {
		return wire.Keypair{}, fmt.Errorf("generate agent private key: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return wire.Keypair{}, fmt.Errorf("create agent private key: %w", err)
	}
	defer file.Close()
	if _, err := file.Write(keypair.Private[:]); err != nil {
		return wire.Keypair{}, fmt.Errorf("write agent private key: %w", err)
	}
	return keypair, nil
}

// SaveReleaseSigningPublicKey persists the master's release-manifest signing
// key. This is a distinct mechanism from node identity — it authenticates
// signed sing-box release manifests (see VerifyReleaseTask) and is unrelated
// to the Noise handshake.
func SaveReleaseSigningPublicKey(dataDir string, publicKeyPEM []byte) error {
	if len(publicKeyPEM) == 0 {
		return nil
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("create agent data directory: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, releaseSigningPublicKeyFile), publicKeyPEM, 0o644); err != nil {
		return fmt.Errorf("write release signing public key: %w", err)
	}
	return nil
}
