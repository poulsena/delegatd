package github

import (
	"crypto/rsa"
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

// loadPrivateKey validates the app configuration and loads its RSA key while
// keeping the safe operator-facing reason separate from the underlying cause.
func loadPrivateKey(cfg Config, dir string) (*rsa.PrivateKey, string, error) {
	if cfg.AppID <= 0 {
		return nil, "app_id must be positive", nil
	}
	if cfg.PrivateKeyFile == "" {
		return nil, "private_key_file is required", nil
	}
	keyPath := cfg.PrivateKeyFile
	if !filepath.IsAbs(keyPath) {
		keyPath = filepath.Join(dir, keyPath)
	}
	info, err := os.Stat(keyPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxPrivateKeySize {
		if err == nil {
			err = errors.New("private key file is not a usable regular file")
		}
		return nil, "private key file is unreadable", err
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, "private key file permissions are too broad", nil
	}
	keyBytes, err := os.ReadFile(keyPath)
	if err != nil || len(keyBytes) > maxPrivateKeySize {
		return nil, "private key file is unreadable", err
	}
	key, err := parseRSAKey(keyBytes)
	if err != nil {
		return nil, "private key is invalid", err
	}
	return key, "", nil
}
