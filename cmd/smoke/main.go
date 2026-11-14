// Command smoke verifies the Compose deployment without running a load test.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

func main() {
	base := flag.String("url", "http://localhost:8090", "Nginx base URL")
	prom := flag.String("prometheus", "http://localhost:9090", "Prometheus base URL")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := check(ctx, strings.TrimRight(*base, "/"), strings.TrimRight(*prom, "/")); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("PASS: 3 instances, 5 allowed, 7 blocked, 3 healthy Prometheus targets")
}

func check(ctx context.Context, base, prom string) error {
	client := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	key := "compose-smoke:" + hex.EncodeToString(nonce[:])
	instances := map[string]bool{}
	allowed, blocked := 0, 0
	for i := 0; i < 12; i++ {
		body, err := json.Marshal(map[string]string{"key": key, "rule": "login"})
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/check", bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		var result struct {
			Allowed   bool  `json:"allowed"`
			Limit     int   `json:"limit"`
			Remaining int   `json:"remaining"`
			Retry     int64 `json:"retry_after_ms"`
			Degraded  bool  `json:"degraded"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 8192)).Decode(&result)
		_ = resp.Body.Close()
		if err != nil {
			return fmt.Errorf("check %d returned invalid JSON: %w", i, err)
		}
		instance := resp.Header.Get("X-RateLimit-Instance")
		if instance == "" {
			return fmt.Errorf("check %d has no upstream instance header", i)
		}
		instances[instance] = true
		if result.Degraded || result.Limit != 5 || resp.Header.Get("X-Request-ID") == "" {
			return fmt.Errorf("check %d did not return an authoritative login decision", i)
		}
		switch resp.StatusCode {
		case http.StatusOK:
			if !result.Allowed || result.Retry != 0 {
				return fmt.Errorf("inconsistent allowed check %d", i)
			}
			allowed++
		case http.StatusTooManyRequests:
			if result.Allowed || result.Retry <= 0 || resp.Header.Get("Retry-After") == "" {
				return fmt.Errorf("inconsistent denied check %d", i)
			}
			blocked++
		default:
			return fmt.Errorf("check %d returned HTTP %d", i, resp.StatusCode)
		}
	}
	if len(instances) != 3 || allowed != 5 || blocked != 7 {
		return fmt.Errorf("shared quota mismatch: instances=%d allowed=%d blocked=%d", len(instances), allowed, blocked)
	}
	query := prom + "/api/v1/query?query=" + url.QueryEscape(`up{job="ratelimiter"}`)
	for {
		if healthyTargets(ctx, client, query) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("prometheus did not report three healthy limiter targets: %w", ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func healthyTargets(ctx context.Context, client *http.Client, target string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	var result struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Value []json.RawMessage `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 65536)).Decode(&result); err != nil || resp.StatusCode != 200 || result.Status != "success" || len(result.Data.Result) != 3 {
		return false
	}
	for _, sample := range result.Data.Result {
		if len(sample.Value) != 2 {
			return false
		}
		var value string
		if json.Unmarshal(sample.Value[1], &value) != nil || value != "1" {
			return false
		}
	}
	return true
}
