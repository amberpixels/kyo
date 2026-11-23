// Package callback ships kyo's Ansible callback plugin inside the binary and
// installs it where ansible-playbook can load it.
package callback

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Name is the plugin's file stem and CALLBACK_NAME.
const Name = "kyo"

//go:embed kyo.py
var source []byte

// Source returns the plugin's Python source.
func Source() []byte { return source }

// Install writes the plugin into a content-addressed dir under cacheRoot and
// returns that dir, ready for ANSIBLE_CALLBACK_PLUGINS. A dir that already holds
// this exact source is reused, so repeat runs write nothing.
func Install(cacheRoot string) (string, error) {
	sum := sha256.Sum256(source)
	dir := filepath.Join(cacheRoot, "kyo", hex.EncodeToString(sum[:8]), "callback")
	file := filepath.Join(dir, Name+".py")

	if existing, err := os.ReadFile(file); err == nil && bytes.Equal(existing, source) {
		return dir, nil
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("creating plugin dir: %w", err)
	}
	if err := writeAtomic(dir, file); err != nil {
		return "", fmt.Errorf("installing plugin: %w", err)
	}
	return dir, nil
}

// DefaultCacheRoot is the user cache dir (XDG_CACHE_HOME or the OS default),
// falling back to the temp dir when neither resolves.
func DefaultCacheRoot() string {
	if d, err := os.UserCacheDir(); err == nil {
		return d
	}
	return os.TempDir()
}

// writeAtomic writes the source to a temp file in dir and renames it over file,
// so a concurrent kyo never loads a half-written plugin.
func writeAtomic(dir, file string) error {
	tmp, err := os.CreateTemp(dir, Name+".*.tmp")
	if err != nil {
		return err
	}
	_, err = tmp.Write(source)
	err = errors.Join(err, tmp.Close())
	if err == nil {
		err = os.Rename(tmp.Name(), file)
	}
	if err != nil {
		_ = os.Remove(tmp.Name()) // best effort; the error that matters is err
	}
	return err
}
