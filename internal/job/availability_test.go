package job

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestWorkerListingConcurrentTimeouts(t *testing.T) {
	started := make(chan struct{}, 3)
	var mu sync.Mutex
	active, peak := 0, 0
	slow := endpoint(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		active++
		peak = max(peak, active)
		mu.Unlock()
		defer func() { mu.Lock(); active--; mu.Unlock() }()
		started <- struct{}{}
		<-r.Context().Done()
	}))
	fast := endpoint(t, worker(t, 2).Handler())
	c := NewCoordinator(Paths{State: t.TempDir()}, Config{Workers: []WorkerConfig{
		{Name: "slow-a", Socket: slow}, {Name: "healthy", Socket: fast}, {Name: "slow-b", Socket: slow},
	}}, testBinary)
	defer c.Close()
	rpc := NewRPC(endpoint(t, c.Handler(context.Background())))
	defer rpc.Close()
	type receipt struct {
		Name   string
		Health Health
		Error  string
	}
	var out []receipt
	begin := time.Now()
	if err := rpc.Call(context.Background(), "GET", "/workers?timeout=150ms", nil, &out); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(begin); elapsed > time.Second {
		t.Fatalf("listing took %s", elapsed)
	}
	if len(out) != 3 || out[0].Name != "slow-a" || out[1].Name != "healthy" || out[2].Name != "slow-b" || out[0].Error == "" || out[2].Error == "" || out[1].Error != "" || out[1].Health.Slots != 2 {
		t.Fatalf("receipts: %+v", out)
	}
	if len(started) != 2 {
		t.Fatal("probes did not both reach worker")
	}
	mu.Lock()
	concurrent := peak == 2
	mu.Unlock()
	if !concurrent {
		t.Fatal("worker probes ran sequentially")
	}
	for _, value := range []string{"0", "-1s", "invalid"} {
		response := httptest.NewRecorder()
		c.Handler(context.Background()).ServeHTTP(response, httptest.NewRequest("GET", "/workers?timeout="+value, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", value, response.Code)
		}
	}
}

func TestHealthAvailabilityBudget(t *testing.T) {
	for _, timeout := range []time.Duration{0, 40 * time.Millisecond} {
		t.Run(timeout.String(), func(t *testing.T) {
			socket := endpoint(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
			transport := &Transport{Config: WorkerConfig{Socket: socket}, Timeout: timeout}
			defer transport.Close()
			start := time.Now()
			_, err := transport.Health(context.Background(), Request{})
			budget := timeout
			if budget == 0 {
				budget = time.Second
			}
			if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) < budget/2 || time.Since(start) > budget+500*time.Millisecond {
				t.Fatalf("timeout=%s elapsed=%s err=%v", timeout, time.Since(start), err)
			}
			if err := transport.lock(context.Background()); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			defer cancel()
			_, err = transport.Health(ctx, Request{})
			transport.unlock()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
		})
	}
}

func TestSchedulingAvailabilityConfiguration(t *testing.T) {
	for _, tc := range []struct {
		timeout   string
		available bool
	}{{"20ms", false}, {"300ms", true}} {
		t.Run(tc.timeout, func(t *testing.T) {
			socket := endpoint(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case <-time.After(80 * time.Millisecond):
					respond(w, Health{Protocol: Protocol, Slots: 2})
				case <-r.Context().Done():
				}
			}))
			config := Config{Coordinator: CoordinatorSettings{WorkerTimeout: new(tc.timeout)}, Workers: []WorkerConfig{{Name: "worker", Socket: socket}}}
			c := NewCoordinator(Paths{State: t.TempDir()}, config, testBinary)
			defer c.Close()
			candidates, busy, err := c.candidates(context.Background(), Record{Request: request(t, "true")})
			if err != nil || busy || (len(candidates) == 1) != tc.available {
				t.Fatalf("available=%v busy=%v err=%v", len(candidates), busy, err)
			}
		})
	}
}
