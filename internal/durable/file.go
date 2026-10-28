// Package durable replaces files atomically and persists their directory entries.
package durable

import (
	"errors"
	"os"
	"path/filepath"
)

// WriteFile replaces path only after the complete contents and mode are synced.
func WriteFile(path string, data []byte, mode os.FileMode) error {
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".write-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name()) // The temporary name disappears after rename.
	if _, err = f.Write(data); err != nil {
		return errors.Join(err, f.Close())
	}
	if err = f.Chmod(mode); err != nil {
		return errors.Join(err, f.Close())
	}
	if err = errors.Join(f.Sync(), f.Close()); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

// EnsureDir persists newly created directories in parent-first order.
func EnsureDir(path string) error {
	var missing []string
	for p := path; ; p = filepath.Dir(p) {
		_, err := os.Stat(p)
		if err == nil {
			break
		}
		if !os.IsNotExist(err) {
			return err
		}
		missing = append(missing, p)
		if filepath.Dir(p) == p {
			break
		}
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if err := syncDir(filepath.Dir(missing[i])); err != nil {
			return err
		}
	}
	return nil
}

func syncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}
