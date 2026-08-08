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

// configurationState remembers both sides of a deploy: the hash the control
// plane asked for, and the hash of what the node actually wrote. They differ
// whenever the node adapts a configuration to the host — moving a service
// behind the SNI router, merging routes for sites already on the port. The
// control plane is told the desired hash, so it sees a converged node; the
// effective hash is what proves the file has not been changed since.
type configurationState struct {
	DesiredHash   string `json:"desired_hash"`
	EffectiveHash string `json:"effective_hash"`
}

func saveConfigurationState(dataDir, name, configurationPath, desiredHash string) error {
	effectiveHash, err := configurationFileHash(configurationPath)
	if err != nil {
		return err
	}
	content, err := json.Marshal(configurationState{DesiredHash: strings.ToLower(desiredHash), EffectiveHash: effectiveHash})
	if err != nil {
		return err
	}
	directory := filepath.Join(dataDir, "desired-state")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, "."+strings.TrimSuffix(name, ".json")+"-*.json")
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
	return os.Rename(temporaryPath, filepath.Join(directory, name))
}

// reportedConfigurationHash is the desired hash to report, but only while the
// file on disk still matches what this node wrote. Anything else — an edit by
// hand, a half-finished deploy — reports nothing, which asks the control plane
// to send the configuration again.
func reportedConfigurationHash(dataDir, name, configurationPath string) string {
	content, err := os.ReadFile(filepath.Join(dataDir, "desired-state", name))
	if err != nil {
		return ""
	}
	var state configurationState
	if json.Unmarshal(content, &state) != nil || len(state.DesiredHash) != sha256.Size*2 || len(state.EffectiveHash) != sha256.Size*2 {
		return ""
	}
	actualHash, err := configurationFileHash(configurationPath)
	if err != nil || !strings.EqualFold(actualHash, state.EffectiveHash) {
		return ""
	}
	return strings.ToLower(state.DesiredHash)
}

func saveNginxConfigurationState(dataDir, desiredHash string) error {
	return saveConfigurationState(dataDir, "nginx.json", managedNginxConfig, desiredHash)
}

func reportedNginxConfigurationHash(dataDir string) string {
	return reportedConfigurationHash(dataDir, "nginx.json", managedNginxConfig)
}

func saveSingBoxConfigurationState(dataDir, desiredHash string) error {
	return saveConfigurationState(dataDir, "singbox.json", managedSingBoxConfig, desiredHash)
}

func reportedSingBoxConfigurationHash(dataDir string) string {
	return reportedConfigurationHash(dataDir, "singbox.json", managedSingBoxConfig)
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
