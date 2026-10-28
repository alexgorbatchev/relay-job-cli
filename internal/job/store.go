package job

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/alexgorbatchev/relay-jobs-cli/internal/durable"
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
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return durable.WriteFile(path, b, 0600)
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
	if err := durable.EnsureDir(filepath.Dir(path)); err != nil {
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
