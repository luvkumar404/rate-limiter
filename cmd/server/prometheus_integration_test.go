//go:build integration && prometheus

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func prometheusDocker(t *testing.T, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", args...).Output()
	if err != nil {
		t.Fatalf("docker %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// This opt-in Docker Desktop test verifies ingestion by an actual Prometheus,
// not just exposition formatting. It creates and removes its own container.
func TestPrometheusLiveScrape(t *testing.T) {
	temp := t.TempDir()
	rules := filepath.Join(temp, "rules.yaml")
	if err := os.WriteFile(rules, []byte("rules:\n  api:\n    algorithm: token_bucket\n    limit: 2\n    window: 1h\n"), 0600); err != nil {
		t.Fatal(err)
	}
	addresses := make(chan string, 1)
	logger := slog.New(addressHandler{Handler: slog.NewJSONHandler(io.Discard, nil), address: addresses})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, "0.0.0.0:0", rules, logger) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("server did not stop")
		}
	})
	var address string
	select {
	case address = <-addresses:
	case <-time.After(6 * time.Second):
		t.Fatal("server did not start")
	}
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 2 * time.Second}
	defer client.CloseIdleConnections()
	for i := 0; i < 3; i++ {
		resp, err := client.Post("http://127.0.0.1:"+port+"/check", "application/json", strings.NewReader(`{"key":"scrape-proof","rule":"api"}`))
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
			t.Fatalf("traffic %d: %d want %d", i, resp.StatusCode, want)
		}
	}
	configPath := filepath.Join(temp, "prometheus.yaml")
	config := fmt.Sprintf("global:\n  scrape_interval: 1s\nscrape_configs:\n  - job_name: ratelimiter-smoke\n    static_configs:\n      - targets: ['host.docker.internal:%s']\n", port)
	if err := os.WriteFile(configPath, []byte(config), 0644); err != nil {
		t.Fatal(err)
	}
	id := prometheusDocker(t, "run", "--detach", "--rm", "--publish", "127.0.0.1::9090", "--mount", "type=bind,source="+configPath+",target=/etc/prometheus/prometheus.yml,readonly", "prom/prometheus:v3.2.1", "--config.file=/etc/prometheus/prometheus.yml")
	t.Cleanup(func() { prometheusDocker(t, "rm", "--force", id) })
	prometheusAddress := prometheusDocker(t, "port", id, "9090/tcp")
	query := func(expr string) (float64, bool) {
		resp, err := client.Get("http://" + prometheusAddress + "/api/v1/query?query=" + url.QueryEscape(expr))
		if err != nil {
			return 0, false
		}
		defer resp.Body.Close()
		var body struct {
			Status string `json:"status"`
			Data   struct {
				Result []struct {
					Value []json.RawMessage `json:"value"`
				} `json:"result"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.Status != "success" || len(body.Data.Result) != 1 || len(body.Data.Result[0].Value) != 2 {
			return 0, false
		}
		var value string
		if err := json.Unmarshal(body.Data.Result[0].Value[1], &value); err != nil {
			return 0, false
		}
		n, err := strconv.ParseFloat(value, 64)
		return n, err == nil
	}
	want := map[string]float64{"up": 1, `checks_total{result="allowed"}`: 2, `checks_total{result="blocked"}`: 1, "check_duration_seconds_count": 3, "redis_errors_total": 0, "circuit_state": 0}
	deadline := time.Now().Add(20 * time.Second)
	for {
		matched := true
		for expr, n := range want {
			if got, ok := query(expr); !ok || got != n {
				matched = false
				break
			}
		}
		if matched {
			break
		}
		if time.Now().After(deadline) {
			if resp, err := client.Get("http://" + prometheusAddress + "/api/v1/targets"); err == nil {
				body, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				t.Logf("Prometheus targets: %s", body)
			}
			t.Fatal("Prometheus did not ingest the expected metrics within 20 seconds")
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Log("Prometheus scraped the live server: up=1, allowed=2, blocked=1, histogram count=3, Redis errors=0, circuit=closed")
}
