package durable

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new", "nested", "file")
	if err := WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	old, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	if err := WriteFile(path, []byte("replacement"), 0755); err != nil {
		t.Fatal(err)
	}
	before, err := io.ReadAll(old)
	if err != nil || string(before) != "original" {
		t.Fatal("existing inode was truncated", string(before), err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != "replacement" {
		t.Fatal(string(after), err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatal(info, err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatal("temporary file leaked", entries, err)
	}
}

func TestFailedReplacementPreservesDestination(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(target, "keep")
	if err := os.WriteFile(keep, []byte("preserved"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(target, []byte("replacement"), 0600); err == nil {
		t.Fatal("replaced a directory")
	}
	got, err := os.ReadFile(keep)
	if err != nil || !bytes.Equal(got, []byte("preserved")) {
		t.Fatal(string(got), err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatal("temporary file leaked", entries, err)
	}
	if err := WriteFile(filepath.Join(keep, "child"), nil, 0600); err == nil {
		t.Fatal("accepted non-directory parent")
	}
	if os.Geteuid() != 0 {
		if err := os.Chmod(target, 0500); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(target, 0700)
		if err := WriteFile(keep, []byte("replacement"), 0600); err == nil {
			t.Fatal("ignored unwritable directory")
		}
		got, err := os.ReadFile(keep)
		if err != nil || string(got) != "preserved" {
			t.Fatal(string(got), err)
		}
	}
}
