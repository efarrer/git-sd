package github

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeGHCreate installs a fake gh whose `pr create` runs createBody and whose
// `pr view` prints a PR as JSON; every call is logged so tests can count them.
func fakeGHCreate(t *testing.T, createBody string) (logPath string) {
	t.Helper()
	logPath = filepath.Join(t.TempDir(), "calls.log")
	withFakeGH(t, fmt.Sprintf(`
echo "$*" >> %q
case "$1 $2" in
  "pr create") %s ;;
  "pr view") echo '{"number":9,"url":"https://github.com/o/r/pull/9","state":"OPEN"}' ;;
  *) echo "unexpected: $*" >&2; exit 1 ;;
esac
`, logPath, createBody))
	return logPath
}

func TestCreatePRNeedsNoFollowUpLookup(t *testing.T) {
	cases := []struct {
		name       string
		create     string
		wantNumber int
		wantCalls  int
	}{
		{"url only", `echo https://github.com/o/r/pull/123`, 123, 1},
		{"progress noise before the url", `echo "Creating pull request for x into main in o/r"; echo; echo https://github.com/o/r/pull/7`, 7, 1},
		{"enterprise host", `echo https://ghe.example.com/o/r/pull/55`, 55, 1},
		{"no recognisable url falls back to a lookup", `echo "created"`, 9, 2},
		{"already exists falls back to a lookup", `echo "a pull request for branch x already exists:" >&2; echo https://github.com/o/r/pull/9 >&2; exit 1`, 9, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			logPath := fakeGHCreate(t, c.create)
			info, err := CreatePR("t", "b", "main", "x")
			if err != nil {
				t.Fatalf("CreatePR: %v", err)
			}
			if info.Number != c.wantNumber || info.State != "OPEN" || !strings.Contains(info.URL, fmt.Sprintf("/pull/%d", c.wantNumber)) {
				t.Errorf("CreatePR = %+v, want number %d, OPEN, matching url", info, c.wantNumber)
			}
			data, _ := os.ReadFile(logPath)
			if got := len(strings.Split(strings.TrimSpace(string(data)), "\n")); got != c.wantCalls {
				t.Errorf("gh was called %d times, want %d:\n%s", got, c.wantCalls, data)
			}
		})
	}
}

func TestNewPRFromCreateOutputRejectsNonPRURLs(t *testing.T) {
	for _, out := range []string{"", "created", "https://github.com/o/r", "https://github.com/o/r/pull/abc", "https://github.com/o/r/issues/4"} {
		if info, ok := newPRFromCreateOutput(out); ok {
			t.Errorf("newPRFromCreateOutput(%q) = %+v, want no match", out, info)
		}
	}
}
