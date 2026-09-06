package server

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func localCredentialKey(directory string, allowCreate bool) (string, error) {
	path := filepath.Join(directory, "credential-master-key")
	// Reuse the development key for databases previously created by make dev.
	for _, candidate := range []string{path, filepath.Join(directory, "dev-credential-master-key")} {
		value, err := os.ReadFile(candidate)
		if err == nil {
			encoded := strings.TrimSpace(string(value))
			key, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil || len(key) != 32 {
				return "", fmt.Errorf("invalid local credential key in %s; restore the original key", candidate)
			}
			return encoded, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	if !allowCreate {
		return "", fmt.Errorf("existing cloud connections require their original STEWARD_CREDENTIAL_MASTER_KEY; refusing to generate a replacement key")
	}
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		return "", err
	}
	encoded := base64.StdEncoding.EncodeToString(key[:])
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return localCredentialKey(directory, false)
	}
	if err != nil {
		return "", err
	}
	if _, err := fmt.Fprintln(file, encoded); err != nil {
		file.Close()
		return "", err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return "", err
	}
	return encoded, file.Close()
}
