package requestid

import (
	"context"
	"strings"
	"testing"
)

func TestIDs(t *testing.T) {
	for _, id := range []string{"", "white space", "line\nbreak", "comma,id", "こんにちは", strings.Repeat("x", 129)} {
		if Valid(id) {
			t.Fatalf("invalid ID accepted: %q", id)
		}
	}
	if !Valid("client-123.a_b:c") {
		t.Fatal("bounded ASCII ID rejected")
	}
	a, b := New(), New()
	if !Valid(a) || !Valid(b) || a == b {
		t.Fatal("generated IDs are invalid or reused")
	}
	if FromContext(context.Background()) != "" || FromContext(WithContext(context.Background(), a)) != a {
		t.Fatal("context propagation failed")
	}
}
