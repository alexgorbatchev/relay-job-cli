package job

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Health struct {
	Protocol int      `json:"protocol"`
	Busy     int      `json:"busy"`
	Slots    int      `json:"slots"`
	Warm     bool     `json:"warm"`
	Missing  []string `json:"missing,omitempty"`
}

const preparedLease = 5 * time.Minute

type Worker struct {
	Store         Store
	Cache, Binary string
	Rsync         string // verified absolute path; sshd's command PATH may differ
	Slots         int
	Local         bool
	Retention     Retention
	mu            sync.Mutex
	checked       map[string]time.Time
	active        map[string]bool
}

func (w *Worker) recordsLocked() ([]Record, error) {
	if w.active == nil {
		records, err := w.Store.List()
		if err != nil {
			return nil, err
		}
		w.active = map[string]bool{}
		for _, r := range records {
			if !r.Terminal() {
				w.active[r.ID] = true
			}
		}
	}
	var records []Record
	for id := range w.active {
		r, err := w.Store.Get(id)
		if err != nil {
			return nil, err
		}
		if r.Terminal() {
			delete(w.active, id)
			delete(w.checked, id)
		} else {
			records = append(records, r)
		}
	}
	return records, nil
}

func (w *Worker) workspace(r Request) string {
	if w.Local {
		return r.Source
	}
	return filepath.Join(w.Cache, "workspaces", WorkspaceKey(r.Source))
}
func (w *Worker) Prepare(r Request) (Record, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := r.Validate(); err != nil {
		return Record{}, err
	}
	if old, err := w.Store.Get(r.ID); err == nil {
		if !sameRequest(old.Request, r) {
			return old, fmt.Errorf("job ID already belongs to another request")
		}
		lock, err := recordLock(w.Store.Path(r.ID, "job.lock"))
		if err != nil {
			return old, err
		}
		defer lock.Close()
		old, err = w.Store.Get(r.ID)
		if err != nil {
			return old, err
		}
		if old.State == statePrepared {
			if time.Now().After(old.LeaseUntil) {
				old.State = stateCanceled
				old.ExitCode = 130
			} else {
				old.LeaseUntil = time.Now().Add(preparedLease)
			}
			err = w.Store.Put(old)
		}
		return old, err
	} else if !os.IsNotExist(err) {
		return old, err
	}
	workspaceLock, err := w.Store.WorkspaceLock(r.Source)
	if err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return Record{}, &APIError{409, "workspace executing"}
		}
		return Record{}, err
	}
	defer workspaceLock.Close()
	records, err := w.recordsLocked()
	if err != nil {
		return Record{}, err
	}
	busy := 0
	for _, other := range records {
		if other.State == statePrepared && time.Now().After(other.LeaseUntil) {
			other, err = w.Cancel(context.Background(), other.ID)
			if err != nil {
				return Record{}, err
			}
		}
		if !other.Terminal() {
			busy++
			if other.Workspace == w.workspace(r) {
				return Record{}, &APIError{409, "workspace busy"}
			}
		}
	}
	if busy >= w.Slots {
		return Record{}, &APIError{409, "worker busy"}
	}
	dir := w.workspace(r)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return Record{}, err
	}
	rec := Record{Request: r, State: statePrepared, Workspace: dir, Created: time.Now().UTC(), ExitCode: -1, LeaseUntil: time.Now().Add(preparedLease), SyncState: w.Store.Dir, SyncBinary: w.Binary, SyncRsync: w.Rsync}
	if err = w.Store.Put(rec); err != nil {
		return rec, err
	}
	w.active[rec.ID] = true
	return rec, nil
}
func (w *Worker) Start(ctx context.Context, id string) (Record, error) {
	if !validID.MatchString(id) {
		return Record{}, &APIError{400, "invalid job ID"}
	}
	lock, err := recordLock(w.Store.Path(id, "job.lock"))
	if err != nil {
		return Record{}, err
	}
	defer lock.Close()
	r, err := w.Store.Get(id)
	if err != nil {
		return r, err
	}
	if r.State != statePrepared {
		return r, nil
	}
	if time.Now().After(r.LeaseUntil) {
		r.State = stateCanceled
		r.ExitCode = 130
		if err = w.Store.Put(r); err != nil {
			return r, err
		}
		return r, &APIError{409, "source-sync reservation expired"}
	}
	r.State = stateStarting
	if err = w.Store.Put(r); err != nil {
		return r, err
	}
	// Persist before handing execution to the native supervisor. Ambiguity never permits replay.
	out, err := startExecution(ctx, w.Binary, w.Store, id)
	if err != nil {
		r.Error = fmt.Sprintf("execution submission uncertain: %v: %s", err, strings.TrimSpace(string(out)))
		if saveErr := w.Store.Put(r); saveErr != nil {
			return r, saveErr
		}
		return r, fmt.Errorf("%s", r.Error)
	}
	return r, nil
}
func (w *Worker) Status(ctx context.Context, id string) (Record, error) {
	r, err := w.Store.Get(id)
	if err != nil {
		return r, err
	}
	if r.State == statePrepared && time.Now().After(r.LeaseUntil) {
		return w.Cancel(ctx, id)
	}
	if r.State != stateStarting && r.State != stateRunning && r.State != stateCanceling {
		if r.Terminal() {
			return r, finishExecution(ctx, w.Store, id)
		}
		return r, nil
	}
	w.mu.Lock()
	if w.checked == nil {
		w.checked = map[string]time.Time{}
	}
	if time.Since(w.checked[id]) < time.Second {
		w.mu.Unlock()
		return r, nil
	}
	w.checked[id] = time.Now()
	w.mu.Unlock()
	active, err := executionActive(ctx, w.Store, id)
	if ctx.Err() != nil {
		return r, ctx.Err()
	}
	if err != nil {
		return r, fmt.Errorf("query execution unit: %w", err)
	}
	if active {
		return r, nil
	}
	lock, err := recordLock(w.Store.Path(id, "job.lock"))
	if err != nil {
		return r, err
	}
	defer lock.Close()
	r, err = w.Store.Get(id)
	if err != nil {
		return r, err
	}
	if r.Terminal() {
		return r, nil
	}
	if r.State == stateCanceling {
		r.State = stateCanceled
		r.ExitCode = 130
	} else {
		r.State = stateLost
		r.ExitCode = 125
		r.Error = "execution unit disappeared without a completion receipt; job will not be replayed"
	}
	return r, w.Store.Put(r)
}
func (w *Worker) Cancel(ctx context.Context, id string) (Record, error) {
	if !validID.MatchString(id) {
		return Record{}, &APIError{400, "invalid job ID"}
	}
	lock, err := recordLock(w.Store.Path(id, "job.lock"))
	if err != nil {
		return Record{}, err
	}
	r, err := w.Store.Get(id)
	if err != nil {
		lock.Close()
		return r, err
	}
	if r.Terminal() {
		lock.Close()
		return r, nil
	}
	prepared := r.State == statePrepared
	r.State = stateCanceling
	err = w.Store.Put(r)
	lock.Close()
	if err != nil {
		return r, err
	}
	if !prepared {
		out, stopErr := stopExecution(ctx, w.Store, id)
		if stopErr != nil {
			latest, statusErr := w.Status(ctx, id)
			if statusErr != nil || !latest.Terminal() {
				return r, fmt.Errorf("stop job: %w: %s", stopErr, out)
			}
		}
	}
	lock, err = recordLock(w.Store.Path(id, "job.lock"))
	if err != nil {
		return r, err
	}
	defer lock.Close()
	r, err = w.Store.Get(id)
	if err != nil {
		return r, err
	}
	r.State = stateCanceled
	r.ExitCode = 130
	return r, w.Store.Put(r)
}
func (w *Worker) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(out http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		records, err := w.recordsLocked()
		w.mu.Unlock()
		if err != nil {
			apiFailure(out, err)
			return
		}
		h := Health{Protocol: Protocol, Slots: w.Slots}
		for _, rec := range records {
			if !rec.Terminal() {
				latest, statusErr := w.Status(r.Context(), rec.ID)
				if statusErr != nil {
					apiFailure(out, statusErr)
					return
				}
				if !latest.Terminal() {
					h.Busy++
				}
			}
		}
		_, err = os.Stat(filepath.Join(w.Cache, "workspaces", WorkspaceKey(r.URL.Query().Get("source"))))
		h.Warm = err == nil
		for _, name := range r.URL.Query()["requires"] {
			if _, err = exec.LookPath(name); err != nil {
				h.Missing = append(h.Missing, name)
			}
		}
		respond(out, h)
	})
	mux.HandleFunc("POST /jobs", func(out http.ResponseWriter, r *http.Request) {
		var req Request
		if !decode(out, r, &req) {
			return
		}
		rec, err := w.Prepare(req)
		if err != nil {
			apiFailure(out, err)
			return
		}
		respond(out, rec)
	})
	mux.HandleFunc("POST /jobs/{id}/start", func(out http.ResponseWriter, r *http.Request) {
		rec, err := w.Start(r.Context(), r.PathValue("id"))
		if err != nil {
			apiFailure(out, err)
			return
		}
		respond(out, rec)
	})
	mux.HandleFunc("POST /jobs/{id}/cancel", func(out http.ResponseWriter, r *http.Request) {
		rec, err := w.Cancel(r.Context(), r.PathValue("id"))
		if err != nil {
			apiFailure(out, err)
			return
		}
		respond(out, rec)
	})
	mux.HandleFunc("GET /jobs/{id}", func(out http.ResponseWriter, r *http.Request) {
		rec, err := w.Status(r.Context(), r.PathValue("id"))
		if err != nil {
			apiFailure(out, err)
			return
		}
		respond(out, rec)
	})
	mux.HandleFunc("POST /jobs/{id}/collected", func(out http.ResponseWriter, r *http.Request) {
		rec, err := w.collectLogs(r.PathValue("id"))
		if err != nil {
			apiFailure(out, err)
			return
		}
		respond(out, rec)
	})
	mux.HandleFunc("GET /jobs/{id}/log", w.Store.ServeLog)
	return mux
}
func apiFailure(w http.ResponseWriter, err error) {
	code := 500
	var api *APIError
	if errors.As(err, &api) {
		code = api.Status
	} else if os.IsNotExist(err) {
		code = 404
	}
	http.Error(w, err.Error(), code)
}
func (s Store) ServeLog(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validID.MatchString(id) {
		http.Error(w, "invalid ID", 400)
		return
	}
	stream := r.URL.Query().Get("stream")
	if stream != "stdout" && stream != "stderr" {
		http.Error(w, "invalid stream", 400)
		return
	}
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || offset < 0 {
		http.Error(w, "invalid offset", 400)
		return
	}
	lock, err := recordLock(s.Path(id, "job.lock"))
	if err != nil {
		apiFailure(w, err)
		return
	}
	defer lock.Close()
	rec, err := s.Get(id)
	if err != nil {
		apiFailure(w, err)
		return
	}
	if rec.LogsExpired {
		http.Error(w, "job logs expired by retention policy", http.StatusGone)
		return
	}
	f, err := os.Open(s.Path(id, stream))
	if os.IsNotExist(err) {
		w.Header().Set("X-Log-Size", "0")
		return
	}
	if err != nil {
		apiFailure(w, err)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		apiFailure(w, err)
		return
	}
	if !info.Mode().IsRegular() {
		http.Error(w, "job log is not a regular file", 500)
		return
	}
	if offset > info.Size() {
		http.Error(w, "job log was truncated", 409)
		return
	}
	if _, err = f.Seek(offset, io.SeekStart); err != nil {
		apiFailure(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	length := min(info.Size()-offset, int64(1<<20))
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	w.Header().Set("X-Log-Size", strconv.FormatInt(info.Size(), 10))
	if r.Method == "HEAD" {
		return
	}
	// Bound each poll so stdout cannot starve stderr and cancellation.
	if _, err = io.CopyN(w, f, length); err != nil {
		return
	}
}
