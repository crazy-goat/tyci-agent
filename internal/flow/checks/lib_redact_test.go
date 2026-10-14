package checks

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// redactIn runs redact from lib_redact.sh on input and returns its output.
func redactIn(t *testing.T, input string) string {
	t.Helper()
	lib, err := filepath.Abs("lib_redact.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", `. "$1"; redact`, "bash", lib)
	cmd.Stdin = strings.NewReader(input)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("redact: %v", err)
	}
	return out.String()
}

// Token fixtures are built at run time, so the source has no secret-looking literal.
// leak is the part of the input that must not reach the output.
func TestRedactLib(t *testing.T) {
	cases := []struct {
		name, input, leak string
	}{
		{"github token", "ghp_" + strings.Repeat("a", 36), "ghp_"},
		{"fine-grained token", "github_pat_" + strings.Repeat("B", 40), "github_pat_"},
		{"sk key", "sk-" + strings.Repeat("c", 30), "sk-c"},
		{"aws key id", "AKIA" + strings.Repeat("D", 16), "AKIADDD"},
		{"bearer header", "Authorization: Bearer " + strings.Repeat("e", 20), "eeeeeeeeee"},
		{"env token", "MY_API_TOKEN=" + strings.Repeat("f", 20), "ffffffffff"},
		{"env password", "DB_PASSWORD=hunter2", "hunter2"},
		{"env secret", "CLIENT_SECRET=abc123", "abc123"},
		{"env api key", "OPENAI_API_KEY=xyz789", "xyz789"},
		{"github server token", "ghs_" + strings.Repeat("h", 36), "ghs_"},
		{"github oauth token", "gho_" + strings.Repeat("i", 36), "gho_"},
		{"yaml token", "MY_API_TOKEN: " + strings.Repeat("g", 20), "ggggggggg"},
		{"yaml password", "DB_PASSWORD: hunter3", "hunter3"},
		{"private key", "-----BEGIN RSA PRIVATE KEY-----\nMIIEow\n-----END RSA PRIVATE KEY-----", "MIIEow"},
		{"unended private key", "-----BEGIN PRIVATE KEY-----\nMIIEow", "MIIEow"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := redactIn(t, "before\n"+c.input+"\nafter\n")
			if strings.Contains(got, c.leak) {
				t.Fatalf("secret kept: %q", got)
			}
			if !strings.Contains(got, "<redacted") {
				t.Fatalf("no marker: %q", got)
			}
			if !strings.HasPrefix(got, "before\n") {
				t.Fatalf("line before the secret lost: %q", got)
			}
		})
	}
}

// The private key rule gives the same output on GNU and BSD sed, so the output is compared exactly.
func TestRedactPrivateKeyExact(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"ended", "a\n-----BEGIN RSA PRIVATE KEY-----\nbody\n-----END RSA PRIVATE KEY-----\nb\n", "a\n<redacted private key>\nb\n"},
		{"unended", "a\n-----BEGIN PRIVATE KEY-----\nbody\n", "a\n<redacted private key>\n"},
		{"two blocks", "-----BEGIN PRIVATE KEY-----\nx\n-----END PRIVATE KEY-----\nm\n-----BEGIN PRIVATE KEY-----\ny\n-----END PRIVATE KEY-----\n", "<redacted private key>\nm\n<redacted private key>\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := redactIn(t, c.in); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestRedactLeavesPlainText(t *testing.T) {
	in := "go test ./... ok\nPASS: TestRedact\nTOKENS are counted here\n"
	if got := redactIn(t, in); got != in {
		t.Fatalf("plain text changed: %q", got)
	}
}
