package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

type nginxConfigurationState struct {
	DesiredHash   string `json:"desired_hash"`
	EffectiveHash string `json:"effective_hash"`
}

func saveNginxConfigurationState(dataDir, desiredHash string) error {
	effectiveHash, err := configurationFileHash(managedNginxConfig)
	if err != nil {
		return err
	}
	state := nginxConfigurationState{DesiredHash: strings.ToLower(desiredHash), EffectiveHash: effectiveHash}
	content, err := json.Marshal(state)
	if err != nil {
		return err
	}
	directory := filepath.Join(dataDir, "desired-state")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".nginx-*.json")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, filepath.Join(directory, "nginx.json"))
}

func reportedNginxConfigurationHash(dataDir string) string {
	content, err := os.ReadFile(filepath.Join(dataDir, "desired-state", "nginx.json"))
	if err != nil {
		return ""
	}
	var state nginxConfigurationState
	if json.Unmarshal(content, &state) != nil || len(state.DesiredHash) != sha256.Size*2 || len(state.EffectiveHash) != sha256.Size*2 {
		return ""
	}
	actualHash, err := configurationFileHash(managedNginxConfig)
	if err != nil || !strings.EqualFold(actualHash, state.EffectiveHash) {
		return ""
	}
	return strings.ToLower(state.DesiredHash)
}

func configurationFileHash(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:]), nil
}
