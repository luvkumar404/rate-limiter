package middleware_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/luvkumar404/rate-limiter/internal/api"
	core "github.com/luvkumar404/rate-limiter/internal/limiter"
	"github.com/luvkumar404/rate-limiter/pkg/limiter"
	"github.com/luvkumar404/rate-limiter/pkg/middleware"
)

func TestEmbeddedAndRemoteSharedEnforcement(t *testing.T) {
	for _, mode := range []string{"embedded", "remote"} {
		t.Run(mode, func(t *testing.T) {
			bucket, err := limiter.NewTokenBucket(limiter.TokenBucketConfig{Limit: 20, Burst: 5, Window: 24 * time.Hour})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = bucket.Close() }()
			var bases []middleware.Limiter
			if mode == "embedded" {
				bases = []middleware.Limiter{bucket, bucket}
			} else {
				service := httptest.NewServer(api.New(map[string]core.Limiter{"api": bucket}, quiet()))
				defer service.Close()
				for i := 0; i < 2; i++ {
					remote, err := middleware.NewRemote(service.URL, "api", middleware.RemoteOptions{Timeout: 5 * time.Second})
					if err != nil {
						t.Fatal(err)
					}
					bases = append(bases, remote)
				}
			}
			var apps []http.Handler
			var hits atomic.Int64
			for _, base := range bases {
				protect, err := middleware.New(base, middleware.Options{Key: middleware.Header("X-API-Key"), Timeout: 5 * time.Second, Logger: quiet()})
				if err != nil {
					t.Fatal(err)
				}
				apps = append(apps, protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.WriteHeader(204) })))
			}
			jobs := make(chan int, 200)
			for i := 0; i < 200; i++ {
				jobs <- i
			}
			close(jobs)
			var blocked, failures atomic.Int64
			var wg sync.WaitGroup
			for i := 0; i < 16; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for n := range jobs {
						r := httptest.NewRequest("GET", "/orders", nil)
						r.Header.Set("X-API-Key", "shared-user")
						w := httptest.NewRecorder()
						apps[n%2].ServeHTTP(w, r)
						switch w.Code {
						case 204:
						case 429:
							blocked.Add(1)
						default:
							failures.Add(1)
						}
					}
				}()
			}
			wg.Wait()
			if hits.Load() != 25 || blocked.Load() != 175 || failures.Load() != 0 {
				t.Fatalf("shared enforcement: app hits=%d blocked=%d failures=%d", hits.Load(), blocked.Load(), failures.Load())
			}
		})
	}
}

func TestRemoteProtocolValidation(t *testing.T) {
	allowed := `{"allowed":true,"limit":100,"remaining":119,"retry_after_ms":0}`
	blocked := `{"allowed":false,"limit":100,"remaining":0,"retry_after_ms":1500}`
	for _, tc := range []struct {
		name              string
		status            int
		body, contentType string
		valid             bool
	}{
		{"burst exceeds steady limit", 200, allowed, "application/json", true},
		{"blocked", 429, blocked, "application/json; charset=utf-8", true},
		{"degraded", 200, `{"allowed":true,"limit":100,"remaining":0,"retry_after_ms":0,"degraded":true}`, "application/json", true},
		{"future field", 200, strings.TrimSuffix(allowed, "}") + `,"future":1}`, "application/json", true},
		{"missing fields", 200, `{}`, "application/json", false},
		{"null fields", 200, `{"allowed":null}`, "application/json", false},
		{"invalid json", 200, `{`, "application/json", false},
		{"trailing json", 200, allowed + ` {}`, "application/json", false},
		{"wrong status decision", 200, blocked, "application/json", false},
		{"wrong blocked decision", 429, allowed, "application/json", false},
		{"zero retry", 429, strings.Replace(blocked, "1500", "0", 1), "application/json", false},
		{"overflow retry", 429, strings.Replace(blocked, "1500", "9223372036854775807", 1), "application/json", false},
		{"negative remaining", 200, strings.Replace(allowed, "119", "-1", 1), "application/json", false},
		{"allowed with retry", 200, strings.Replace(allowed, `"retry_after_ms":0`, `"retry_after_ms":1`, 1), "application/json", false},
		{"invalid degradation", 429, strings.TrimSuffix(blocked, "}") + `,"degraded":true}`, "application/json", false},
		{"oversized", 200, strings.Repeat(" ", 8193), "application/json", false},
		{"content type", 200, allowed, "text/plain", false},
		{"unavailable", 503, `{"error":"private backend details"}`, "application/json", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "POST" || r.URL.Path != "/prefix/check" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer s.Close()
			remote, err := middleware.NewRemote(s.URL+"/prefix/", "api", middleware.RemoteOptions{Timeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			result, err := remote.Allow(context.Background(), "user")
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v result=%+v err=%v", tc.valid, result, err)
			}
			if err != nil && strings.Contains(err.Error(), "private") {
				t.Fatal("backend body leaked in error")
			}
			if calls.Load() != 1 {
				t.Fatalf("remote check retried %d times", calls.Load())
			}
			if tc.name == "burst exceeds steady limit" && result.Remaining != 119 {
				t.Fatal("valid burst remaining was clamped")
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRemoteTimeoutRedirectsAndIDs(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })}
	remote, err := middleware.NewRemote("http://example.test", "api", middleware.RemoteOptions{Client: client, Timeout: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote.Allow(context.Background(), "user"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout not propagated: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := remote.Allow(ctx, "user"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	var redirected atomic.Int64
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/check" {
			http.Redirect(w, r, "/redirected", http.StatusTemporaryRedirect)
		} else {
			redirected.Add(1)
			w.WriteHeader(500)
		}
	}))
	defer s.Close()
	sentinel := errors.New("caller redirect policy")
	provided := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return sentinel }}
	remote, err = middleware.NewRemote(s.URL, "api", middleware.RemoteOptions{Client: provided, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote.Allow(context.Background(), "user"); err == nil || redirected.Load() != 0 {
		t.Fatal("remote followed redirect")
	}
	if provided.CheckRedirect(nil, nil) != sentinel {
		t.Fatal("caller client mutated")
	}
	receivedID := make(chan string, 1)
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedID <- r.Header.Get("X-Request-ID")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"allowed":true,"limit":5,"remaining":4,"retry_after_ms":0}`)
	}))
	defer service.Close()
	remote, _ = middleware.NewRemote(service.URL, "api", middleware.RemoteOptions{Timeout: time.Second})
	protect, _ := middleware.New(remote, middleware.Options{Timeout: time.Second, Logger: quiet()})
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Request-ID", "caller-correlation")
	w := httptest.NewRecorder()
	protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })).ServeHTTP(w, r)
	id := <-receivedID
	if w.Code != 204 || id != "caller-correlation" || w.Header().Get("X-Request-ID") != id {
		t.Fatal("ID not forwarded through remote check")
	}
}

func TestRemoteConstructorValidation(t *testing.T) {
	for _, base := range []string{"", "localhost:8082", "ftp://host", "http://:8082", "http://user:pass@host", "http://host?q=1", "http://host?", "http://host#fragment", "http://host/%2Fprefix"} {
		if _, err := middleware.NewRemote(base, "api", middleware.RemoteOptions{}); err == nil {
			t.Fatalf("invalid URL accepted: %s", base)
		}
	}
	for _, rule := range []string{"", " api ", strings.Repeat("x", 129)} {
		if _, err := middleware.NewRemote("http://example.test", rule, middleware.RemoteOptions{}); err == nil {
			t.Fatal("invalid rule accepted")
		}
	}
	if _, err := middleware.NewRemote("http://example.test", "api", middleware.RemoteOptions{Timeout: -1}); err == nil {
		t.Fatal("negative timeout accepted")
	}
	remote, _ := middleware.NewRemote("http://example.test", "api", middleware.RemoteOptions{})
	for _, key := range []string{"", strings.Repeat("x", 513)} {
		if _, err := remote.Allow(context.Background(), key); err == nil {
			t.Fatal("invalid key accepted")
		}
	}
}

func ExampleNew() {
	bucket, _ := limiter.NewTokenBucket(limiter.TokenBucketConfig{Limit: 5, Window: time.Minute})
	defer func() { _ = bucket.Close() }()
	protect, _ := middleware.New(bucket, middleware.Options{Key: middleware.ClientIP()})
	handler := protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, "hello") }))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/hello", nil))
	fmt.Println(w.Code, w.Body.String())
	// Output: 200 hello
}
