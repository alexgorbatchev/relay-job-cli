package job

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type Store struct{ Dir string }

func (s Store) WorkspaceLock(source string) (*os.File, error) {
	return Lock(filepath.Join(s.Dir, "workspaces", WorkspaceKey(source)+".lock"))
}

func (s Store) Path(id, name string) string { return filepath.Join(s.Dir, "jobs", id, name) }
func (s Store) Get(id string) (Record, error) {
	var r Record
	if !validID.MatchString(id) {
		return r, fmt.Errorf("invalid job ID")
	}
	err := ReadJSON(s.Path(id, "record.json"), &r)
	return r, err
}
func (s Store) Put(r Record) error {
	if err := r.Request.Validate(); err != nil {
		return err
	}
	r.Updated = time.Now().UTC()
	return WriteJSON(s.Path(r.ID, "record.json"), r)
}
func WriteJSON(path string, v any) error {
	if err := ensureDir(filepath.Dir(path)); err != nil {
		return err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".write-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name()) // temporary name disappears after successful rename.
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func (s Store) List() ([]Record, error) {
	entries, err := os.ReadDir(filepath.Join(s.Dir, "jobs"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Record
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		r, err := s.Get(e.Name())
		if os.IsNotExist(err) {
			if _, markerErr := os.Stat(s.Path(e.Name(), "started")); os.IsNotExist(markerErr) {
				continue
			}
		}
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}
func Lock(path string) (*os.File, error) {
	return fileLock(path, syscall.LOCK_EX|syscall.LOCK_NB)
}
func recordLock(path string) (*os.File, error) { return fileLock(path, syscall.LOCK_EX) }
func fileLock(path string, how int) (*os.File, error) {
	if err := ensureDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), how); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func ensureDir(path string) error {
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
	// Persist new directory entries before a receipt can authorize execution.
	for i := len(missing) - 1; i >= 0; i-- {
		d, err := os.Open(filepath.Dir(missing[i]))
		if err != nil {
			return err
		}
		err = d.Sync()
		closeErr := d.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
