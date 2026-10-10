package git

import (
	"strings"
	"testing"
)

func TestAuthHint(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   bool
	}{
		{"no username prompt", "fatal: could not read Username for 'https://github.com': terminal prompts disabled", true},
		{"authentication failed", "remote: Invalid username or password.\nfatal: Authentication failed for 'https://github.com/o/r.git/'", true},
		{"terminal prompts disabled", "fatal: could not read Password for 'https://github.com': terminal prompts disabled", true},
		{"credential helper gh missing", "git: 'credential-gh' is not a git command\n/usr/bin/git-credential-gh: gh: command not found", true},
		{"credential helper gh not found (sh)", "sh: 1: gh: not found", true},
		{"unrelated network failure", "fatal: unable to access 'https://github.com/o/r.git/': Could not resolve host: github.com", false},
		{"rejected non-fast-forward", " ! [rejected] main -> main (non-fast-forward)", false},
		{"empty", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hint := AuthHint(c.output)
			if (hint != "") != c.want {
				t.Fatalf("AuthHint(%q) = %q, want hint=%v", c.output, hint, c.want)
			}
			if c.want && !strings.Contains(hint, "gh auth login") {
				t.Fatalf("hint should mention 'gh auth login': %q", hint)
			}
		})
	}
}
