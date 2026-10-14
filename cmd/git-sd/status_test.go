package main

import (
	"errors"
	"github.com/efarrer/git-sd/internal/github"
	"github.com/efarrer/git-sd/internal/ui"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stubPRLookups replaces getDetailedPRInfo for one test. delay is how long each
// lookup takes; failFor lists PR numbers whose lookup fails. It returns
// counters for total calls and the highest number of lookups in flight at once.
func stubPRLookups(t *testing.T, delay time.Duration, failFor map[int]bool) (calls, maxInFlight *atomic.Int64) {
	t.Helper()
	calls, maxInFlight = &atomic.Int64{}, &atomic.Int64{}
	var inFlight atomic.Int64
	var mu sync.Mutex
	orig := getDetailedPRInfo
	t.Cleanup(func() { getDetailedPRInfo = orig })
	getDetailedPRInfo = func(n int) (*github.DetailedPRInfo, error) {
		calls.Add(1)
		cur := inFlight.Add(1)
		mu.Lock()
		if cur > maxInFlight.Load() {
			maxInFlight.Store(cur)
		}
		mu.Unlock()
		defer inFlight.Add(-1)
		time.Sleep(delay)
		if failFor[n] {
			return nil, errors.New("lookup failed")
		}
		return &github.DetailedPRInfo{PRInfo: github.PRInfo{Number: n, State: "OPEN"}}, nil
	}
	return calls, maxInFlight
}

func TestFetchPRInfosRunsLookupsConcurrently(t *testing.T) {
	calls, _ := stubPRLookups(t, 100*time.Millisecond, nil)
	numbers := []int{11, 12, 13, 14, 15, 16}

	start := time.Now()
	got := fetchPRInfos(numbers)
	elapsed := time.Since(start)

	// Six serial lookups would take 600 ms; concurrent ones take about 100 ms.
	if elapsed > 350*time.Millisecond {
		t.Fatalf("6 lookups of 100 ms took %v; they should run concurrently", elapsed)
	}
	if calls.Load() != 6 || len(got) != 6 {
		t.Fatalf("calls=%d results=%d, want 6 and 6", calls.Load(), len(got))
	}
	for _, n := range numbers {
		if lk, ok := got[n]; !ok || lk.err != nil || lk.info == nil || lk.info.Number != n {
			t.Fatalf("result for PR %d = %+v (ok=%v), want its own info", n, got[n], ok)
		}
	}
}

func TestFetchPRInfosRespectsTheConcurrencyCap(t *testing.T) {
	calls, maxInFlight := stubPRLookups(t, 30*time.Millisecond, nil)
	var numbers []int
	for n := 1; n <= 3*maxConcurrentPRLookups; n++ {
		numbers = append(numbers, n)
	}

	got := fetchPRInfos(numbers)

	if int(calls.Load()) != len(numbers) || len(got) != len(numbers) {
		t.Fatalf("calls=%d results=%d, want %d each", calls.Load(), len(got), len(numbers))
	}
	if max := maxInFlight.Load(); max > maxConcurrentPRLookups || max < 2 {
		t.Fatalf("max lookups in flight = %d, want between 2 and the cap %d", max, maxConcurrentPRLookups)
	}
}

func TestFetchPRInfosKeepsFailuresPerPR(t *testing.T) {
	stubPRLookups(t, time.Millisecond, map[int]bool{2: true, 4: true})

	got := fetchPRInfos([]int{1, 2, 3, 4})

	for _, n := range []int{1, 3} {
		if got[n].err != nil || got[n].info == nil {
			t.Fatalf("PR %d should have succeeded: %+v", n, got[n])
		}
	}
	for _, n := range []int{2, 4} {
		if got[n].err == nil {
			t.Fatalf("PR %d should have failed: %+v", n, got[n])
		}
	}
}

func TestFetchPRInfosWithNoPRsMakesNoCalls(t *testing.T) {
	calls, _ := stubPRLookups(t, time.Millisecond, nil)

	if got := fetchPRInfos(nil); len(got) != 0 {
		t.Fatalf("results = %v, want none", got)
	}
	if calls.Load() != 0 {
		t.Fatalf("made %d lookups for no PRs", calls.Load())
	}
}

func TestPRMarkerNeverGuessesAFailedLookup(t *testing.T) {
	lookups := map[int]prLookup{
		1: {info: &github.DetailedPRInfo{PRInfo: github.PRInfo{State: "MERGED"}}},
		2: {err: errors.New("GraphQL: Something went wrong")},
		3: {}, // no error but no info either
	}
	cases := []struct {
		name     string
		prNumber int
		want     ui.MarkerID
	}{
		{"no PR yet is Assigned", 0, ui.Assigned},
		{"a readable PR shows its state", 1, ui.Merged},
		{"a failed lookup is unavailable, not Assigned", 2, ui.Unavailable},
		{"a lookup with no data is unavailable", 3, ui.Unavailable},
		{"a PR that was never looked up is unavailable", 99, ui.Unavailable},
	}
	for _, c := range cases {
		if got := prMarkerID(lookups, c.prNumber); got != c.want {
			t.Errorf("%s: prMarkerID(%d) = %q, want %q", c.name, c.prNumber, got, c.want)
		}
	}
}

func TestLookupFailureWarnings(t *testing.T) {
	ok := prLookup{info: &github.DetailedPRInfo{}}
	boom := prLookup{err: errors.New("boom")}
	later := prLookup{err: errors.New("later")}
	env := prLookup{err: github.ErrGHNotAuthenticated}

	cases := []struct {
		name    string
		numbers []int
		results map[int]prLookup
		want    []string
	}{
		{"nothing failed", []int{1, 2}, map[int]prLookup{1: ok, 2: ok}, nil},
		{"no PRs", nil, nil, nil},
		{"one failure names the PR and the error", []int{12, 13}, map[int]prLookup{12: boom, 13: ok},
			[]string{"Warning: could not read the state of PR #12: boom"}},
		{"several failures are listed in stack order with the first error", []int{14, 12, 13}, map[int]prLookup{14: boom, 12: later, 13: ok},
			[]string{"Warning: could not read the state of PR #14, #12: boom"}},
		{"an environment error keeps its own warning", []int{1, 2}, map[int]prLookup{1: env, 2: env},
			[]string{"Warning: PR states unavailable: You are not authenticated with 'gh'. Please run 'gh auth login'."}},
		{"both kinds produce both lines", []int{1, 2}, map[int]prLookup{1: env, 2: boom},
			[]string{
				"Warning: PR states unavailable: You are not authenticated with 'gh'. Please run 'gh auth login'.",
				"Warning: could not read the state of PR #2: boom",
			}},
	}
	for _, c := range cases {
		got := summarizeLookups(c.numbers, c.results).warnings()
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got  %q\n want %q", c.name, got, c.want)
		}
	}
}

func TestLegendRequested(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		configured string
		want       bool
	}{
		{"hidden by default", nil, "false", false},
		{"config true shows it", nil, "true", true},
		{"--legend shows it", []string{"--legend"}, "false", true},
		{"--no-legend overrides config", []string{"--no-legend"}, "true", false},
		{"the last flag wins", []string{"--legend", "--no-legend"}, "false", false},
		{"unknown config value is false", nil, "yes", false},
	}
	for _, c := range cases {
		if got := legendRequested(c.args, c.configured); got != c.want {
			t.Errorf("%s: legendRequested(%v, %q) = %v, want %v", c.name, c.args, c.configured, got, c.want)
		}
	}
}
