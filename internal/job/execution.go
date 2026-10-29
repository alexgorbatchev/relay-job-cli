package job

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func Execute(ctx context.Context, s Store, id string) error {
	if !validID.MatchString(id) {
		return fmt.Errorf("invalid job ID")
	}
	// Held until every other lock is released, so stopping can wait for full cleanup.
	supervisor, err := Lock(s.Path(id, "execution.lock"))
	if err != nil {
		return err
	}
	defer supervisor.Close()
	lock, err := recordLock(s.Path(id, "job.lock"))
	if err != nil {
		return err
	}
	rec, err := s.Get(id)
	if err != nil {
		lock.Close()
		return err
	}
	if rec.State != stateStarting {
		lock.Close()
		return nil
	}
	workspaceLock, err := s.WorkspaceLock(rec.Source)
	if err != nil {
		lock.Close()
		return err
	}
	defer workspaceLock.Close()
	// Exclusive marker survives crashes and transient-unit collection: this ID can never execute twice.
	marker, err := os.OpenFile(s.Path(id, "started"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		lock.Close()
		return err
	}
	if err = marker.Sync(); err != nil {
		marker.Close()
		lock.Close()
		return err
	}
	marker.Close()
	rec.State = stateRunning
	err = s.Put(rec)
	lock.Close()
	if err != nil {
		return err
	}
	code, runErr := runCommand(ctx, s, rec)
	lock, err = recordLock(s.Path(id, "job.lock"))
	if err != nil {
		return err
	}
	defer lock.Close()
	current, err := s.Get(id)
	if err != nil {
		return err
	}
	current.ExitCode = code
	if current.State == stateCanceling || ctx.Err() != nil {
		current.State = stateCanceled
		current.ExitCode = 130
	} else if code == 0 {
		current.State = stateSucceeded
	} else {
		current.State = stateFailed
	}
	if runErr != nil {
		current.Error = runErr.Error()
	}
	return s.Put(current)
}
func runDirectCommand(ctx context.Context, s Store, r Record) (int, error) {
	out, err := os.OpenFile(s.Path(r.ID, "stdout"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return 125, err
	}
	defer out.Close()
	stderr, err := os.OpenFile(s.Path(r.ID, "stderr"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return 125, err
	}
	defer stderr.Close()
	dir, program, err := prepareCommand(r)
	if err != nil {
		return 125, err
	}
	cmd := exec.CommandContext(ctx, program, r.Spec.Command[1:]...)
	cmd.Dir = dir
	cmd.Stdout = out
	cmd.Stderr = stderr
	cmd.Env = os.Environ()
	for k, v := range r.Spec.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	err = cmd.Run()
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	} // terminate remaining descendants before releasing the workspace.
	if syncErr := out.Sync(); syncErr != nil {
		return 125, syncErr
	}
	if syncErr := stderr.Sync(); syncErr != nil {
		return 125, syncErr
	}
	if err == nil {
		return 0, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code := exit.ExitCode()
		if code < 0 {
			code = 128 + int(exit.Sys().(syscall.WaitStatus).Signal())
		}
		return code, nil
	}
	return 125, err
}

func prepareCommand(r Record) (string, string, error) {
	dir, err := filepath.EvalSymlinks(filepath.Join(r.Workspace, r.Spec.Cwd))
	if err != nil {
		return "", "", err
	}
	root, err := filepath.EvalSymlinks(r.Workspace)
	if err != nil {
		return "", "", err
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil || !filepath.IsLocal(rel) {
		return "", "", fmt.Errorf("cwd escapes workspace")
	}
	search := os.Getenv("PATH")
	if value, ok := r.Spec.Env["PATH"]; ok {
		search = value
	}
	program, err := resolveExecutable(r.Spec.Command[0], dir, search)
	return dir, program, err
}

func resolveExecutable(name, dir, search string) (string, error) {
	// exec.Command resolves against the runner's PATH before Cmd.Env and Cmd.Dir
	// apply. Check absolute candidates with LookPath to use the job's environment.
	if strings.ContainsRune(name, os.PathSeparator) {
		return name, nil
	}
	var denied error
	for _, base := range filepath.SplitList(search) {
		if !filepath.IsAbs(base) {
			base = filepath.Join(dir, base)
		}
		path, err := exec.LookPath(filepath.Join(base, name))
		if err == nil {
			return path, nil
		}
		if errors.Is(err, os.ErrPermission) {
			denied = err
		}
	}
	if denied != nil {
		return "", denied
	}
	return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
}
