package main

import (
	"git-sd/internal/github"
	"strconv"
	"sync"
)

// maxConcurrentPRCreates bounds how many `gh pr create` calls sync runs at once.
// Creates are slow (~2.2 s) and independent once their branches are pushed.
const maxConcurrentPRCreates = 4

// getPRInfo is github.GetPRInfo; tests replace it.
var getPRInfo = github.GetPRInfo

// prStateLookup is the outcome of looking up one PR's state (merged, closed or
// open, with its base and merge commit) for sync and rebase.
type prStateLookup struct {
	info *github.PRInfo
	err  error
}

// stackLookup is the outcome of reading one registered Stack's PR list.
type stackLookup struct {
	prs []int
	err error
}

// fetchStacks reads every registered Stack in numbers concurrently, keyed by
// Stack number.
func fetchStacks(numbers []int) map[int]stackLookup {
	return fetchConcurrently(numbers, func(n int) stackLookup {
		prs, err := github.GetStack(n)
		return stackLookup{prs: prs, err: err}
	})
}

// fetchPRStates looks up every PR in numbers concurrently and returns the
// outcomes keyed by number. Callers read the map in their own (stack) order, so
// decisions, warnings and errors come out exactly as if the lookups had been
// made one at a time.
func fetchPRStates(numbers []int) map[int]prStateLookup {
	return fetchConcurrently(numbers, func(n int) prStateLookup {
		info, err := getPRInfo(strconv.Itoa(n))
		return prStateLookup{info: info, err: err}
	})
}

// fetchConcurrently runs fetch for every distinct number, at most
// maxConcurrentPRLookups at a time, and returns the results keyed by number.
// With no numbers it makes no calls.
func fetchConcurrently[T any](numbers []int, fetch func(int) T) map[int]T {
	var distinct []int
	seen := map[int]bool{}
	for _, n := range numbers {
		if !seen[n] {
			seen[n] = true
			distinct = append(distinct, n)
		}
	}

	results := make([]T, len(distinct))
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxConcurrentPRLookups)
	for i, n := range distinct {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = fetch(n)
		}()
	}
	wg.Wait()

	byNumber := make(map[int]T, len(distinct))
	for i, n := range distinct {
		byNumber[n] = results[i]
	}
	return byNumber
}
