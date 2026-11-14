package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthProbe(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable, http.StatusFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
			defer server.Close()
			err := probe(context.Background(), server.URL)
			if (err == nil) != (status == 200) {
				t.Fatalf("probe HTTP %d: %v", status, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := probe(ctx, "http://127.0.0.1:1"); err == nil {
		t.Fatal("canceled probe succeeded")
	}
}
