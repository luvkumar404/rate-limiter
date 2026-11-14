package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDecode(t *testing.T) {
	cfg, err := Decode(strings.NewReader("rules:\n  api:\n    algorithm: token_bucket\n    limit: 10\n    window: 1m\n    burst: 2\n"))
	if err != nil {
		t.Fatal(err)
	}
	r := cfg.Rules["api"]
	if r.Limit != 10 || r.Burst != 2 || r.Window != time.Minute || r.Scope != "key" || cfg.FailurePolicy != "fail_open" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestInvalidConfig(t *testing.T) {
	valid := "rules:\n  api:\n    algorithm: token_bucket\n    limit: 10\n    window: 1m\n"
	for name, input := range map[string]string{
		"empty": "", "no rules": "rules: {}", "bad yaml": "[",
		"unknown root field":   valid + "typo: 1\n",
		"unknown rule field":   valid + "    typo: 1\n",
		"unknown algorithm":    strings.Replace(valid, "token_bucket", "sliding_window", 1),
		"zero limit":           strings.Replace(valid, "limit: 10", "limit: 0", 1),
		"negative limit":       strings.Replace(valid, "limit: 10", "limit: -1", 1),
		"zero window":          strings.Replace(valid, "window: 1m", "window: 0s", 1),
		"bad window":           strings.Replace(valid, "window: 1m", "window: tomorrow", 1),
		"missing algorithm":    strings.Replace(valid, "    algorithm: token_bucket\n", "", 1),
		"negative burst":       valid + "    burst: -1\n",
		"overflowing refill":   strings.Replace(valid, "window: 1m", "window: 2562047h47m16.854775807s", 1) + "    burst: 10\n",
		"bad scope":            valid + "    scope: invalid\n",
		"bad policy":           valid + "failure_policy: sometimes\n",
		"extra document":       valid + "---\n{}\n",
		"empty extra document": valid + "---\n",
		"duplicate rule":       valid + "  api:\n    limit: 2\n",
		"duplicate field":      valid + "    limit: 2\n",
		"blank name":           strings.Replace(valid, "  api:", "  ' ':", 1),
		"padded name":          strings.Replace(valid, "  api:", "  ' api ':", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(strings.NewReader(input)); err == nil {
				t.Fatalf("accepted invalid config: %s", input)
			}
		})
	}
}

func TestLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.yaml")
	if _, err := Load(path); err == nil {
		t.Fatal("missing file accepted")
	}
	data := "rules:\n  login:\n    algorithm: token_bucket\n    limit: 5\n    window: 1m\n    scope: ip\nfailure_policy: fail_closed\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || cfg.Rules["login"].Scope != "ip" || cfg.FailurePolicy != "fail_closed" {
		t.Fatalf("got %+v, %v", cfg, err)
	}
}
