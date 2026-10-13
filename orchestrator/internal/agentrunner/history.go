package agentrunner

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Native history is installed once, before Pi opens the session. Exclusive
// linking prevents retry or restart from replacing an already continued log.
func installHistory(c Config, nativeFile string) error {
	if c.HistoryDigest == "" {
		return nil
	}
	if raw, err := hex.DecodeString(c.HistoryDigest); err != nil || len(raw) != 32 {
		return errors.New("invalid history digest")
	}
	source := filepath.Join(c.StateRoot, ".history-"+c.Session+".jsonl")
	if _, err := os.Lstat(nativeFile); err == nil {
		if e := os.Remove(source); e != nil && !errors.Is(e, os.ErrNotExist) {
			return e
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	temp, err := os.CreateTemp(filepath.Dir(nativeFile), ".history-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(temp, hash), io.LimitReader(input, (64<<20)+1))
	if err != nil {
		return err
	}
	if n > 64<<20 || fmt.Sprintf("%x", hash.Sum(nil)) != c.HistoryDigest {
		return errors.New("history differs from admitted digest")
	}
	if err = temp.Sync(); err != nil {
		return err
	}
	if err = temp.Close(); err != nil {
		return err
	}
	if err = os.Link(temp.Name(), nativeFile); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	directory, err := os.Open(filepath.Dir(nativeFile))
	if err != nil {
		return err
	}
	defer directory.Close()
	if err = directory.Sync(); err != nil {
		return err
	}
	return os.Remove(source)
}
