package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/alexgorbatchev/relay-jobs-cli/internal/job"
)

func TestWorkerListAvailability(t *testing.T) {
	t.Setenv("AGENT", "1")
	workerSocket := socketServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2200 * time.Millisecond):
			json.NewEncoder(w).Encode(job.Health{Protocol: job.Protocol, Slots: 2})
		case <-r.Context().Done():
		}
	}))
	path, config := configFixture(t)
	config.Coordinator.WorkerTimeout = new("3s")
	config.Workers = []job.WorkerConfig{{Name: "slow", Socket: workerSocket}}
	if err := writeTOML(path, config); err != nil {
		t.Fatal(err)
	}
	c := job.NewCoordinator(job.Paths{State: t.TempDir()}, config, integrationBinary)
	defer c.Close()
	socket := socketServer(t, c.Handler(context.Background()))
	for _, tc := range []struct {
		name   string
		flags  []string
		failed bool
	}{
		{"config-longer-than-old-client-deadline", nil, false},
		{"explicit-override", []string{"--worker-timeout", "30ms"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"--config", path, "--socket", socket, "worker", "list"}, tc.flags...)
			out, stderr, code := execute(context.Background(), args...)
			var receipts []struct {
				Name   string
				Health job.Health
				Error  string
			}
			if code != 0 || stderr != "" || json.Unmarshal([]byte(out), &receipts) != nil || len(receipts) != 1 {
				t.Fatalf("%d %s %s", code, stderr, out)
			}
			if (receipts[0].Error != "") != tc.failed || (!tc.failed && receipts[0].Health.Slots != 2) {
				t.Fatalf("%+v", receipts)
			}
		})
	}
	for _, value := range []string{"0", "-1s", "garbage"} {
		if _, _, code := execute(context.Background(), "worker", "list", "--config", path, "--worker-timeout", value); code == 0 {
			t.Fatalf("accepted %s", value)
		}
	}
}
