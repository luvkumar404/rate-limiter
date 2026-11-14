package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestGracefulShutdownDrainsRequests(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, listener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(entered)
			select {
			case <-release:
			case <-time.After(2 * time.Second):
			}
			_, _ = io.WriteString(w, "finished")
		}), slog.New(slog.NewJSONHandler(io.Discard, nil)))
	}()
	response := make(chan error, 1)
	client := &http.Client{Timeout: 3 * time.Second}
	go func() {
		resp, err := client.Get("http://" + listener.Addr().String())
		if err == nil {
			defer resp.Body.Close()
			var body []byte
			body, err = io.ReadAll(resp.Body)
			if string(body) != "finished" {
				t.Errorf("in-flight response lost: %q", body)
			}
		}
		response <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not enter handler")
	}
	cancel()
	select {
	case err := <-done:
		t.Fatalf("server stopped before request completed: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	unblock()
	select {
	case err := <-response:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("response timed out")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown timed out")
	}
}

func TestRunRejectsMissingConfig(t *testing.T) {
	if err := run(context.Background(), "127.0.0.1:0", "missing.yaml", slog.Default()); err == nil {
		t.Fatal("missing config accepted")
	}
}

type addressHandler struct {
	slog.Handler
	address chan string
}

func (h addressHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Message == "server listening" {
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "address" {
				h.address <- a.Value.String()
			}
			return true
		})
	}
	return h.Handler.Handle(ctx, r)
}

func TestRunEndToEnd(t *testing.T) {
	for _, algorithm := range []string{"token_bucket", "sliding_window", "fixed_window"} {
		t.Run(algorithm, func(t *testing.T) { testRunAlgorithm(t, algorithm) })
	}
}

func testRunAlgorithm(t *testing.T, algorithm string) {
	path := filepath.Join(t.TempDir(), "rules.yaml")
	if err := os.WriteFile(path, []byte(fmt.Sprintf("rules:\n  api:\n    algorithm: %s\n    limit: 2\n    window: 1h\n", algorithm)), 0600); err != nil {
		t.Fatal(err)
	}
	addresses := make(chan string, 1)
	logger := slog.New(addressHandler{Handler: slog.NewJSONHandler(io.Discard, nil), address: addresses})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, "127.0.0.1:0", path, logger) }()
	var address string
	select {
	case address = <-addresses:
	case err := <-done:
		t.Fatalf("server failed: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("server did not start")
	}
	client := &http.Client{Timeout: 3 * time.Second}
	defer client.CloseIdleConnections()
	for i := 0; i < 3; i++ {
		resp, err := client.Post("http://"+address+"/check", "application/json", strings.NewReader(`{"key":"user:42","rule":"api"}`))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		want := 200
		if i == 2 {
			want = 429
		}
		if resp.StatusCode != want {
			t.Fatalf("check %d: got %d want %d", i, resp.StatusCode, want)
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not stop")
	}
}
