package main

import (
	"errors"
	"git-sd/internal/github"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stubPRStates replaces getPRInfo for one test: every call sleeps for delay,
// is counted, and fails for the numbers in failing.
func stubPRStates(t *testing.T, delay time.Duration, failing map[int]error) *atomic.Int32 {
	t.Helper()
	orig := getPRInfo
	t.Cleanup(func() { getPRInfo = orig })
	var calls atomic.Int32
	getPRInfo = func(sel string) (*github.PRInfo, error) {
		calls.Add(1)
		time.Sleep(delay)
		n, _ := strconv.Atoi(sel)
		if err := failing[n]; err != nil {
			return nil, err
		}
		return &github.PRInfo{Number: n, State: "OPEN"}, nil
	}
	return &calls
}

func TestFetchPRStatesRunsLookupsConcurrently(t *testing.T) {
	const delay = 100 * time.Millisecond
	stubPRStates(t, delay, nil)
	start := time.Now()
	got := fetchPRStates([]int{1, 2, 3, 4, 5, 6})
	elapsed := time.Since(start)
	if len(got) != 6 {
		t.Fatalf("got %d results, want 6", len(got))
	}
	for n := 1; n <= 6; n++ {
		if got[n].err != nil || got[n].info == nil || got[n].info.Number != n {
			t.Errorf("PR %d: %+v", n, got[n])
		}
	}
	if elapsed > 4*delay {
		t.Errorf("6 lookups took %v; one at a time they would take %v, concurrently about %v", elapsed, 6*delay, delay)
	}
}

func TestFetchPRStatesKeepsFailuresPerPRAndLooksUpEachPROnce(t *testing.T) {
	boom := errors.New("GraphQL: Something went wrong")
	calls := stubPRStates(t, 0, map[int]error{2: boom})
	got := fetchPRStates([]int{1, 2, 1, 3, 2})
	if calls.Load() != 3 {
		t.Errorf("expected one lookup per distinct PR (3), got %d", calls.Load())
	}
	if !errors.Is(got[2].err, boom) || got[2].info != nil {
		t.Errorf("PR 2 should carry its error: %+v", got[2])
	}
	if got[1].err != nil || got[3].err != nil {
		t.Errorf("a failure must not spread to other PRs: %+v %+v", got[1], got[3])
	}
}

func TestFetchPRStatesWithNoPRsMakesNoCalls(t *testing.T) {
	calls := stubPRStates(t, 0, nil)
	if got := fetchPRStates(nil); len(got) != 0 || calls.Load() != 0 {
		t.Fatalf("expected no results and no calls, got %v / %d", got, calls.Load())
	}
}

func TestFetchPRStatesRespectsTheConcurrencyCap(t *testing.T) {
	var mu sync.Mutex
	running, peak := 0, 0
	orig := getPRInfo
	t.Cleanup(func() { getPRInfo = orig })
	getPRInfo = func(string) (*github.PRInfo, error) {
		mu.Lock()
		running++
		peak = max(peak, running)
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		running--
		mu.Unlock()
		return &github.PRInfo{}, nil
	}
	numbers := make([]int, 40)
	for i := range numbers {
		numbers[i] = i + 1
	}
	fetchPRStates(numbers)
	if peak > maxConcurrentPRLookups {
		t.Errorf("peak concurrency %d exceeds the cap %d", peak, maxConcurrentPRLookups)
	}
}
