//go:build integration && chaos

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func chaosDocker(t *testing.T, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// This test is opt-in and refuses to stop containers outside its dedicated
// Compose project. Normal integration tests never stop or restart Redis.
func TestRedisKillAndRecovery(t *testing.T) {
	container, addr := os.Getenv("REDIS_CHAOS_CONTAINER"), os.Getenv("REDIS_CHAOS_ADDR")
	if container == "" || addr == "" {
		t.Fatal("chaos tests require REDIS_CHAOS_CONTAINER and REDIS_CHAOS_ADDR; see docs/resilience.md")
	}
	project := chaosDocker(t, "inspect", "--format", `{{index .Config.Labels "com.docker.compose.project"}}`, container)
	service := chaosDocker(t, "inspect", "--format", `{{index .Config.Labels "com.docker.compose.service"}}`, container)
	if project != "ratelimiter-phase5-chaos" || service != "redis" {
		t.Fatal("refusing to stop a container outside the dedicated ratelimiter-phase5-chaos Redis project")
	}
	for _, policy := range []string{"fail_open", "fail_closed"} {
		t.Run(policy, func(t *testing.T) {
			chaosDocker(t, "kill", container)
			t.Cleanup(func() { chaosDocker(t, "start", container) })
			path := filepath.Join(t.TempDir(), "rules.yaml")
			data := fmt.Sprintf("backend: redis\nfailure_policy: %s\nredis:\n  addr: %q\n  prefix: chaos:%s:%d\n  operation_timeout: 50ms\ncircuit_breaker:\n  failure_threshold: 2\n  open_timeout: 100ms\nrules:\n", policy, addr, policy, time.Now().UnixNano())
			algorithms := []string{"token_bucket", "sliding_window", "fixed_window"}
			for _, algorithm := range algorithms {
				data += fmt.Sprintf("  %s:\n    algorithm: %s\n    limit: 1\n    window: 1h\n", algorithm, algorithm)
			}
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			url := startRedisInstance(t, path) // Startup must succeed with Redis stopped.
			client := &http.Client{Timeout: 2 * time.Second}
			defer client.CloseIdleConnections()
			getStatus := func(path string) int {
				t.Helper()
				resp, err := client.Get(url + path)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				return resp.StatusCode
			}
			check := func(rule, key string) (int, bool) {
				t.Helper()
				resp, err := client.Post(url+"/check", "application/json", strings.NewReader(fmt.Sprintf(`{"key":%q,"rule":%q}`, key, rule)))
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = resp.Body.Close() }()
				var body struct {
					Degraded  bool `json:"degraded"`
					Allowed   bool `json:"allowed"`
					Limit     int  `json:"limit"`
					Remaining int  `json:"remaining"`
				}
				if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body.Degraded && (!body.Allowed || body.Remaining != 0 || body.Limit != 1) {
					t.Fatalf("invalid fallback: %+v", body)
				}
				return resp.StatusCode, body.Degraded
			}
			assertOutage := func(key string) {
				t.Helper()
				if getStatus("/healthz") != 200 || getStatus("/readyz") != 503 {
					t.Fatal("outage did not preserve liveness and fail readiness")
				}
				for i := 0; i < 4; i++ {
					for _, algorithm := range algorithms {
						status, degraded := check(algorithm, key)
						if policy == "fail_open" {
							if status != 200 || !degraded {
								t.Fatalf("outage fail-open: %d degraded=%v", status, degraded)
							}
						} else if status != 503 || degraded {
							t.Fatalf("outage fail-closed: %d degraded=%v", status, degraded)
						}
					}
				}
			}
			recover := func(key string) {
				t.Helper()
				chaosDocker(t, "start", container)
				deadline := time.Now().Add(6 * time.Second)
				for getStatus("/readyz") != 200 {
					if time.Now().After(deadline) {
						t.Fatal("Redis readiness did not recover")
					}
					time.Sleep(20 * time.Millisecond)
				}
				for _, algorithm := range algorithms {
					for {
						status, degraded := check(algorithm, key)
						if status == 200 && !degraded {
							break
						}
						if time.Now().After(deadline) {
							t.Fatalf("%s circuit did not recover", algorithm)
						}
						time.Sleep(20 * time.Millisecond)
					}
					if status, degraded := check(algorithm, key); status != 429 || degraded {
						t.Fatalf("%s did not resume quota enforcement: %d degraded=%v", algorithm, status, degraded)
					}
				}
			}
			assertOutage("startup")
			recover("healthy")
			chaosDocker(t, "kill", container) // Kill after the service has served real traffic.
			assertOutage("outage")
			recover("recovered")
			t.Log("startup outage and Redis kill during traffic: configured policy, health/readiness, automatic recovery, and resumed quotas verified for all algorithms")
		})
	}
}
