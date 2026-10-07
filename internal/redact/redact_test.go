package redact

import (
	"strings"
	"sync"
	"testing"
)

func TestRedact_Table(t *testing.T) {
	ghp := "ghp_" + strings.Repeat("a", 36)
	pat := "github_pat_" + strings.Repeat("B", 50)
	rows := []struct{ name, in, want string }{
		{"ghp", "token " + ghp + " end", "token [REDACTED] end"},
		{"fine grained", pat, "[REDACTED]"},
		{"sk", "key sk-abcdefghijklmnopqrstuvwxyz", "key [REDACTED]"},
		{"aws", "id AKIAABCDEFGHIJKLMNOP.", "id [REDACTED]."},
		{"bearer", "Authorization: Bearer abc.def-123", "Authorization: Bearer [REDACTED]"},
		{"env", "OPENAI_API_KEY=sk-short", "OPENAI_API_KEY=[REDACTED]"},
		{"env colon", "db_password: hunter2", "db_password: [REDACTED]"},
		{"quoted shell", `export DB_PASSWORD="hunter2hunter2"`, `export DB_PASSWORD="[REDACTED]"`},
		{"single quoted", `DB_PASSWORD='hunter2hunter2'`, `DB_PASSWORD='[REDACTED]'`},
		{"yaml quoted", `  password: "hunter2hunter2"`, `  password: "[REDACTED]"`},
		{"json key", `{"api_key": "abcdefabcdef"}`, `{"api_key": "[REDACTED]"}`},
		{"escaped json", `{"cmd":"export DB_PASSWORD=\"hunter2hunter2\""}`, `{"cmd":"export DB_PASSWORD=\"[REDACTED]\""}`},
		{"private key", "a\n-----BEGIN RSA PRIVATE KEY-----\nxx\nyy\n-----END RSA PRIVATE KEY-----\nb", "a\n[REDACTED]\nb"},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			if got := Redact(r.in); got != r.want {
				t.Errorf("Redact(%q) = %q, want %q", r.in, got, r.want)
			}
		})
	}
}

func TestRedact_NoFalsePositive(t *testing.T) {
	for _, in := range []string{
		"package main\n\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n",
		"commit 8b0ef6501234567890abcdef1234567890abcdef",
		"const TokenTTL = 5",
	} {
		if got := Redact(in); got != in {
			t.Errorf("Redact(%q) = %q, want unchanged", in, got)
		}
	}
}

func TestRedact_ExactTokens(t *testing.T) {
	Add("s3cr3t-value-xyz")
	Add("short") // ignored: under 8 bytes
	got := Redact(`{"k":"s3cr3t-value-xyz","n":"short"}`)
	if got != `{"k":"[REDACTED]","n":"short"}` {
		t.Errorf("got %q", got)
	}
}

func TestRedact_ConcurrentAdd(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); Add("concurrent-secret-" + strings.Repeat("x", i)) }()
		go func() { defer wg.Done(); _ = Redact("some text concurrent-secret-xxx") }()
	}
	wg.Wait()
}
