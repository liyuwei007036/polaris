package control

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/liyuwei007036/polaris/internal/security"
	"github.com/liyuwei007036/polaris/internal/wire"
)

const noisePrivateKeyFile = "master-noise.key.enc"

// LoadOrCreateNoiseKeypair loads the master's persistent Curve25519 identity,
// generating one on first run. This replaces the old X.509 CA: agents pin
// this key's public half out of band (analogous to pinning a CA cert before),
// and the master authenticates itself during the Noise_XK handshake purely by
// proving possession of the matching private key — no certificate involved.
func LoadOrCreateNoiseKeypair(dataDir string, masterKey []byte) (wire.Keypair, error) {
	keyPath := filepath.Join(dataDir, noisePrivateKeyFile)
	encrypted, err := os.ReadFile(keyPath)
	if err == nil {
		plain, err := security.Decrypt(masterKey, encrypted)
		if err != nil {
			return wire.Keypair{}, fmt.Errorf("decrypt master identity key: %w", err)
		}
		if len(plain) != wire.KeySize {
			return wire.Keypair{}, errors.New("master identity key has unexpected length")
		}
		keypair, err := wire.KeypairFromPrivate([wire.KeySize]byte(plain))
		if err != nil {
			return wire.Keypair{}, fmt.Errorf("derive master identity public key: %w", err)
		}
		return keypair, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return wire.Keypair{}, fmt.Errorf("read master identity key: %w", err)
	}
	keypair, err := wire.GenerateKeypair()
	if err != nil {
		return wire.Keypair{}, fmt.Errorf("generate master identity key: %w", err)
	}
	encrypted, err = security.Encrypt(masterKey, keypair.Private[:])
	if err != nil {
		return wire.Keypair{}, fmt.Errorf("encrypt master identity key: %w", err)
	}
	if err := os.WriteFile(keyPath, encrypted, 0o600); err != nil {
		return wire.Keypair{}, fmt.Errorf("write master identity key: %w", err)
	}
	return keypair, nil
}
