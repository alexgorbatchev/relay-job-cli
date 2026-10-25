package job

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

type RPC struct{ Client *http.Client }
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return e.Message }
func NewRPC(socket string) *RPC {
	t := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	return &RPC{Client: &http.Client{Transport: t, Timeout: ConnectTimeout}}
}
func (c *RPC) Close() { c.Client.CloseIdleConnections() }
func (c *RPC) WithTimeout(timeout time.Duration) *RPC {
	client := *c.Client
	client.Timeout = timeout
	return &RPC{Client: &client}
}
func (c *RPC) Call(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		b, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://unix"+path, body)
	if err != nil {
		return err
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, err := io.ReadAll(io.LimitReader(resp.Body, 8192))
		if err != nil {
			return err
		}
		return &APIError{resp.StatusCode, string(bytes.TrimSpace(b))}
	}
	if output != nil {
		return json.NewDecoder(resp.Body).Decode(output)
	}
	_, err = io.Copy(io.Discard, resp.Body)
	return err
}
func (c *RPC) Log(ctx context.Context, id, stream string, offset int64, w io.Writer) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://unix/jobs/%s/log?stream=%s&offset=%d", id, stream, offset), nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		if resp.StatusCode == http.StatusGone {
			return 0, fmt.Errorf("job logs expired by retention policy (HTTP 410)")
		}
		return 0, fmt.Errorf("read %s: HTTP %d", stream, resp.StatusCode)
	}
	return io.Copy(w, resp.Body)
}
func (c *RPC) LogSize(ctx context.Context, id, stream string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, "HEAD", "http://unix/jobs/"+id+"/log?stream="+stream+"&offset=0", nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		if resp.StatusCode == http.StatusGone {
			return 0, fmt.Errorf("job logs expired by retention policy (HTTP 410)")
		}
		return 0, fmt.Errorf("read %s size: HTTP %d", stream, resp.StatusCode)
	}
	return strconv.ParseInt(resp.Header.Get("X-Log-Size"), 10, 64)
}
func Serve(ctx context.Context, socket string, handler http.Handler) error {
	return serve(ctx, socket, handler, nil)
}
func ServeCoordinator(ctx context.Context, socket string, c *Coordinator) error {
	if err := c.Retention.Validate(); err != nil {
		return err
	}
	c.Local.Retention = c.Retention
	return serveMaintained(ctx, socket, c.Retention, c.Handler(ctx), func() error { return c.Start(ctx) }, c.cleanup)
}
func serve(ctx context.Context, socket string, handler http.Handler, start func() error) error {
	lock, err := Lock(socket + ".lock")
	if err != nil {
		return fmt.Errorf("service already running or lock unavailable: %w", err)
	}
	defer lock.Close()
	if err = os.MkdirAll(filepath.Dir(socket), 0700); err != nil {
		return err
	}
	if err = os.Remove(socket); err != nil && !os.IsNotExist(err) {
		return err
	}
	ln, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	defer os.Remove(socket)
	if err = os.Chmod(socket, 0600); err != nil {
		ln.Close()
		return err
	}
	if start != nil {
		if err = start(); err != nil {
			ln.Close()
			return err
		}
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	stopped := make(chan struct{})
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			server.Shutdown(shutdown)
		case <-stopped:
		}
	}()
	err = server.Serve(ln)
	close(stopped)
	<-joined
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}
func respond(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		return
	}
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		http.Error(w, err.Error(), 400)
		return false
	}
	if err := oneJSONValue(d); err != nil {
		http.Error(w, err.Error(), 400)
		return false
	}
	return true
}
