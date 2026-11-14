package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/luvkumar404/rate-limiter/internal/requestid"
)

type RemoteOptions struct {
	// Client is copied; its transport and other shared resources remain caller-owned.
	Client *http.Client
	// Timeout covers the HTTP check and decoding. Zero selects 100ms.
	Timeout time.Duration
}

// RemoteLimiter calls POST /check. It is safe to share across requests. It does
// not retry checks or follow redirects, since a check can consume quota.
type RemoteLimiter struct {
	endpoint string
	rule     string
	client   http.Client
	timeout  time.Duration
}

func NewRemote(baseURL, rule string, opts RemoteOptions) (*RemoteLimiter, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u == nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" {
		return nil, errors.New("middleware: limiter URL must be an HTTP(S) base URL without credentials, query, fragment, or escaped path")
	}
	if strings.TrimSpace(rule) == "" || strings.TrimSpace(rule) != rule || len(rule) > 128 {
		return nil, errors.New("middleware: rule must be nonblank and at most 128 bytes, without surrounding whitespace")
	}
	if opts.Timeout < 0 {
		return nil, errors.New("middleware: remote timeout must be nonnegative")
	}
	if opts.Timeout == 0 {
		opts.Timeout = 100 * time.Millisecond
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/check"
	client := http.Client{}
	if opts.Client != nil {
		client = *opts.Client
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &RemoteLimiter{endpoint: u.String(), rule: rule, client: client, timeout: opts.Timeout}, nil
}

func (l *RemoteLimiter) Allow(ctx context.Context, key string) (Result, error) {
	if strings.TrimSpace(key) == "" || len(key) > 512 {
		return Result{}, errors.New("middleware: key must be nonblank and at most 512 bytes")
	}
	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	body, err := json.Marshal(struct {
		Key  string `json:"key"`
		Rule string `json:"rule"`
	}{key, l.rule})
	if err != nil {
		return Result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.endpoint, bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if id := requestid.FromContext(ctx); id != "" {
		req.Header.Set("X-Request-ID", id)
	}
	resp, err := l.client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("remote rate limiter: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8193))
	if err != nil {
		return Result{}, fmt.Errorf("read rate limit response: %w", err)
	}
	if len(data) > 8192 {
		return Result{}, errors.New("remote rate limiter response exceeds 8192 bytes")
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusTooManyRequests {
		return Result{}, fmt.Errorf("remote rate limiter returned HTTP %d", resp.StatusCode)
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return Result{}, errors.New("remote rate limiter response must be application/json")
	}
	var raw struct {
		Allowed      *bool  `json:"allowed"`
		Limit        *int   `json:"limit"`
		Remaining    *int   `json:"remaining"`
		RetryAfterMS *int64 `json:"retry_after_ms"`
		Degraded     bool   `json:"degraded"`
	}
	d := json.NewDecoder(bytes.NewReader(data))
	if err := d.Decode(&raw); err != nil {
		return Result{}, errors.New("invalid remote rate limiter response")
	}
	var extra any
	if d.Decode(&extra) != io.EOF || raw.Allowed == nil || raw.Limit == nil || raw.Remaining == nil || raw.RetryAfterMS == nil {
		return Result{}, errors.New("incomplete or trailing remote rate limiter response")
	}
	if *raw.Limit <= 0 || *raw.Remaining < 0 || *raw.RetryAfterMS < 0 || *raw.RetryAfterMS > math.MaxInt64/int64(time.Millisecond) || (*raw.Allowed != (resp.StatusCode == http.StatusOK)) || (*raw.Allowed && *raw.RetryAfterMS != 0) || (!*raw.Allowed && *raw.RetryAfterMS == 0) || (raw.Degraded && (!*raw.Allowed || *raw.Remaining != 0)) {
		return Result{}, errors.New("inconsistent remote rate limiter response")
	}
	return Result{Allowed: *raw.Allowed, Limit: *raw.Limit, Remaining: *raw.Remaining, RetryAfter: time.Duration(*raw.RetryAfterMS) * time.Millisecond, Degraded: raw.Degraded}, nil
}
