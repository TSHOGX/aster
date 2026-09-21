package agentrunner

import (
	"errors"
	"io"
	"os"
	"path/filepath"
)

// Project configuration is mounted once. Each logical Session selects its own
// configuration on first launch. Atomic links ensure a crash never selects a
// partial file, and later incarnations preserve the original authored catalog.
func materializeConfig(source, root string) error {
	for _, entry := range []struct{ source, target string }{{"models.json", "authored-models.json"}, {"models.json", "models.json"}, {"settings.json", "settings.json"}} {
		destination := filepath.Join(root, entry.target)
		if _, err := os.Stat(destination); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		from, err := os.Open(filepath.Join(source, entry.source))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		info, err := from.Stat()
		if err != nil {
			from.Close()
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > 1<<20 {
			from.Close()
			return errors.New("agent configuration exceeds bound or is not regular")
		}
		temp, err := os.CreateTemp(root, ".config-*")
		if err != nil {
			from.Close()
			return err
		}
		_, copyErr := io.Copy(temp, io.LimitReader(from, 1<<20))
		syncErr := temp.Sync()
		closeErr := temp.Close()
		from.Close()
		if err = errors.Join(copyErr, syncErr, closeErr); err == nil {
			err = os.Link(temp.Name(), destination)
			if errors.Is(err, os.ErrExist) {
				err = nil
			}
		}
		_ = os.Remove(temp.Name())
		if err != nil {
			return err
		}
		directory, err := os.Open(root)
		if err != nil {
			return err
		}
		syncErr = directory.Sync()
		closeErr = directory.Close()
		if err = errors.Join(syncErr, closeErr); err != nil {
			return err
		}
	}
	return nil
}
