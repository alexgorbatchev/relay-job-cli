package job

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

type Coordinator struct {
	Store     Store
	Workers   []*Transport
	Local     *Worker
	Retention Retention
	mu        sync.Mutex
	jobs      map[string]task
	wg        sync.WaitGroup
	closed    bool
}
type task struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func NewCoordinator(paths Paths, c Config, binary string) *Coordinator {
	s := &Coordinator{Store: Store{paths.State}, jobs: map[string]task{}, Local: &Worker{Store: Store{paths.State + "/local"}, Binary: binary, Slots: 1, Local: true}}
	s.Retention = DefaultRetention()
	s.Local.Retention = s.Retention
	timeout, _ := c.Coordinator.Timeout() // ReadConfig validates public settings.
	for _, w := range c.Workers {
		s.Workers = append(s.Workers, &Transport{Config: w, Timeout: timeout})
	}
	return s
}
func (c *Coordinator) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	records, err := c.Store.List()
	if err != nil {
		return err
	}
	for _, r := range records {
		if !r.Terminal() {
			c.launch(ctx, r)
		}
	}
	return nil
}
func (c *Coordinator) Close() {
	c.mu.Lock()
	c.closed = true
	for _, task := range c.jobs {
		task.cancel()
	}
	c.mu.Unlock()
	c.wg.Wait()
	for _, w := range c.Workers {
		w.Close()
	}
}
func (c *Coordinator) launch(ctx context.Context, r Record) {
	if c.closed {
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	c.jobs[r.ID] = task{cancel, done}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer cancel()
		defer close(done)
		if err := c.run(ctx, r); err != nil {
			slog.Error("job monitor stopped", "id", r.ID, "error", err)
		}
		c.mu.Lock()
		delete(c.jobs, r.ID)
		c.mu.Unlock()
	}()
}
func (c *Coordinator) Submit(ctx context.Context, req Request) (Record, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return Record{}, fmt.Errorf("coordinator is stopping")
	}
	if err := req.Validate(); err != nil {
		return Record{}, &APIError{400, err.Error()}
	}
	info, err := os.Stat(req.Source)
	if err != nil || !info.IsDir() {
		return Record{}, &APIError{400, "source must be an accessible directory"}
	}
	if old, err := c.Store.Get(req.ID); err == nil {
		if !sameRequest(old.Request, req) {
			return old, &APIError{409, "ID already used for another request"}
		}
		return old, nil
	} else if !os.IsNotExist(err) {
		return old, err
	}
	rec := Record{Request: req, State: stateQueued, ExitCode: -1, Created: time.Now().UTC()}
	if err = c.Store.Put(rec); err != nil {
		return rec, err
	}
	c.launch(ctx, rec)
	return rec, nil
}

type candidate struct {
	t *Transport
	h Health
}

func eligible(w WorkerConfig, s Spec) bool {
	if len(s.Workers) > 0 && !slices.Contains(s.Workers, w.Name) {
		return false
	}
	for _, label := range s.Labels {
		if !slices.Contains(w.Labels, label) {
			return false
		}
	}
	return true
}
func (c *Coordinator) candidates(ctx context.Context, r Record) ([]candidate, bool, error) {
	type result struct {
		t   *Transport
		h   Health
		err error
	}
	ch := make(chan result, len(c.Workers))
	count := 0
	for _, w := range c.Workers {
		if !eligible(w.Config, r.Spec) {
			continue
		}
		count++
		go func() { h, err := w.Health(ctx, r.Request); ch <- result{w, h, err} }()
	}
	var out []candidate
	var configErrors []string
	busy := false
	for range count {
		v := <-ch
		if v.err != nil {
			var api *APIError
			if errors.As(v.err, &api) {
				configErrors = append(configErrors, api.Error())
			}
			continue
		}
		if len(v.h.Missing) > 0 {
			configErrors = append(configErrors, v.t.Config.Name+" missing tools: "+strings.Join(v.h.Missing, ","))
			continue
		}
		if v.h.Busy >= v.h.Slots {
			busy = true
			continue
		}
		out = append(out, candidate{v.t, v.h})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].h.Warm != out[j].h.Warm {
			return out[i].h.Warm
		}
		if out[i].h.Busy != out[j].h.Busy {
			return out[i].h.Busy < out[j].h.Busy
		}
		return out[i].t.Config.Name < out[j].t.Config.Name
	})
	if count == 0 {
		return nil, false, fmt.Errorf("no workers match this job")
	}
	if len(out) == 0 && len(configErrors) > 0 {
		return nil, busy, fmt.Errorf("worker configuration: %s", strings.Join(configErrors, "; "))
	}
	return out, busy, nil
}
func (c *Coordinator) assign(ctx context.Context, r *Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	options, busy, err := c.candidates(ctx, *r)
	if err != nil {
		return err
	}
	for _, option := range options {
		var prepared Record
		err = option.t.Call(ctx, "POST", "/jobs", r.Request, &prepared)
		if err != nil {
			var api *APIError
			if errors.As(err, &api) && api.Status == 409 {
				busy = true
				continue
			}
			if errors.As(err, &api) {
				return fmt.Errorf("reserve worker: %w", err)
			}
			continue // A reservation cannot execute; another reachable worker may accept it safely.
		}
		if prepared.State != statePrepared {
			return fmt.Errorf("worker reservation is %s", prepared.State)
		}
		r.Worker = option.t.Config.Name
		r.State = stateSyncing
		r.Workspace = prepared.Workspace
		if err = c.Store.Put(*r); err != nil {
			return err
		}
		if err = syncWorkspace(ctx, option.t, prepared, leaseRefresh); err != nil {
			// No execution has been requested. Release the reservation before considering another host.
			cleanup, cancel := context.WithTimeout(context.Background(), ConnectTimeout)
			cancelErr := option.t.Call(cleanup, "POST", "/jobs/"+r.ID+"/cancel", nil, nil)
			cancel()
			if cancelErr != nil {
				return fmt.Errorf("%w; reservation cleanup: %v", err, cancelErr)
			}
			return err // rsync errors may be permissions or invalid exclusions, never silently run locally.
		}
		r.State = stateStarting
		r.Submitted = true
		if err = c.Store.Put(*r); err != nil {
			return err
		}
		// A start request is never retried on another worker, even when its reply is lost.
		err = option.t.Call(ctx, "POST", "/jobs/"+r.ID+"/start", nil, nil)
		if err != nil {
			r.Error = "start reply unavailable; recovering the same job ID: " + err.Error()
			return c.Store.Put(*r)
		}
		return nil
	}
	if busy {
		return nil
	}
	if r.Spec.Unavailable == "local" {
		prepared, err := c.Local.Prepare(r.Request)
		if err != nil {
			var api *APIError
			if errors.As(err, &api) && api.Status == 409 {
				return nil
			}
			return err
		}
		r.Worker = "local"
		r.Workspace = prepared.Workspace
		r.State = stateStarting
		r.Submitted = true
		if err = c.Store.Put(*r); err != nil {
			return err
		}
		_, err = c.Local.Start(ctx, r.ID)
		if err != nil {
			r.Error = err.Error()
			return c.Store.Put(*r)
		}
		return nil
	}
	return fmt.Errorf("no reachable worker; local fallback is disabled")
}
func (c *Coordinator) worker(name string) *Transport {
	for _, w := range c.Workers {
		if w.Config.Name == name {
			return w
		}
	}
	return nil
}
func (c *Coordinator) status(ctx context.Context, r Record) (Record, error) {
	if r.Worker == "local" {
		return c.Local.Status(ctx, r.ID)
	}
	w := c.worker(r.Worker)
	if w == nil {
		return r, fmt.Errorf("assigned worker %q is absent from configuration", r.Worker)
	}
	var rec Record
	err := w.Call(ctx, "GET", "/jobs/"+r.ID, nil, &rec)
	return rec, err
}

func (c *Coordinator) Cancel(ctx context.Context, id string) (Record, error) {
	c.mu.Lock()
	running, ok := c.jobs[id]
	if ok {
		running.cancel()
	}
	c.mu.Unlock()
	if ok {
		select {
		case <-running.done:
		case <-ctx.Done():
			return Record{}, ctx.Err()
		}
	}
	r, err := c.Store.Get(id)
	if err != nil {
		return r, err
	}
	if r.Terminal() {
		return r, nil
	}
	r.State = stateCanceling
	if err = c.Store.Put(r); err != nil {
		return r, err
	}
	if err = c.cancelWorker(ctx, r); err != nil {
		c.mu.Lock()
		c.launch(context.WithoutCancel(ctx), r)
		c.mu.Unlock()
		if errors.Is(err, errLogsPending) {
			return r, nil
		}
		return r, err
	}
	r.State = stateCanceled
	r.ExitCode = 130
	r.Error = ""
	return r, c.Store.Put(r)
}
func (c *Coordinator) cancelWorker(ctx context.Context, r Record) error {
	var err error
	if r.Worker != "" {
		if r.Worker == "local" {
			_, err = c.Local.Cancel(ctx, r.ID)
		} else if w := c.worker(r.Worker); w != nil {
			var rpc *RPC
			rpc, err = w.Connect(ctx)
			if err == nil {
				err = rpc.WithTimeout(CancelTimeout).Call(ctx, "POST", "/jobs/"+r.ID+"/cancel", nil, nil)
			}
		} else {
			err = fmt.Errorf("assigned worker is not configured")
		}
		if err != nil {
			var api *APIError
			if errors.As(err, &api) && api.Status == 404 && !r.Submitted {
				return nil
			}
			return err
		}
		return c.copyLogs(ctx, r)
	}
	return nil
}
func (c *Coordinator) Handler(ctx context.Context) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /jobs", func(w http.ResponseWriter, r *http.Request) {
		records, err := c.Store.List()
		if err != nil {
			apiFailure(w, err)
			return
		}
		if records == nil {
			records = []Record{}
		}
		slices.SortFunc(records, func(a, b Record) int {
			if order := b.Created.Compare(a.Created); order != 0 {
				return order
			}
			return strings.Compare(a.ID, b.ID)
		})
		respond(w, records)
	})
	mux.HandleFunc("POST /jobs", func(w http.ResponseWriter, r *http.Request) {
		var req Request
		if !decode(w, r, &req) {
			return
		}
		rec, err := c.Submit(ctx, req)
		if err != nil {
			apiFailure(w, err)
			return
		}
		respond(w, rec)
	})
	mux.HandleFunc("GET /jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		rec, err := c.Store.Get(r.PathValue("id"))
		if err != nil {
			apiFailure(w, err)
			return
		}
		respond(w, rec)
	})
	mux.HandleFunc("POST /jobs/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		rec, err := c.Cancel(r.Context(), r.PathValue("id"))
		if err != nil {
			apiFailure(w, err)
			return
		}
		respond(w, rec)
	})
	mux.HandleFunc("GET /jobs/{id}/log", c.Store.ServeLog)
	mux.HandleFunc("GET /workers", func(w http.ResponseWriter, r *http.Request) {
		var timeout time.Duration
		if value := r.URL.Query().Get("timeout"); value != "" {
			var err error
			timeout, err = time.ParseDuration(value)
			if err != nil || timeout <= 0 {
				http.Error(w, "timeout must be a positive duration", http.StatusBadRequest)
				return
			}
		}
		type receipt struct {
			Name   string `json:"name"`
			Health Health `json:"health"`
			Error  string `json:"error,omitempty"`
		}
		out := make([]receipt, len(c.Workers))
		var probes sync.WaitGroup
		for i, worker := range c.Workers {
			probes.Go(func() {
				budget := timeout
				if budget == 0 {
					budget = worker.healthTimeout()
				}
				h, err := worker.health(r.Context(), Request{}, budget)
				out[i] = receipt{Name: worker.Config.Name, Health: h}
				if err != nil {
					out[i].Error = err.Error()
				}
			})
		}
		probes.Wait()
		respond(w, out)
	})
	return mux
}
