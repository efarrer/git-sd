// Package timing is the GIT_SD_TIMING=1 diagnostic: it records how long the
// phases of a command take and how many subprocesses it starts, and prints
// that to stderr. Everything is a no-op unless the variable is set to 1.
package timing

import (
	"fmt"
	"io"
	"os"
	"sort"
	"sync"
	"time"
)

var (
	enabled = os.Getenv("GIT_SD_TIMING") == "1"
	start   = time.Now()

	mu     sync.Mutex
	phases []phase
	spawns = map[string]*spawn{}
)

type phase struct {
	name string
	d    time.Duration
}

type spawn struct {
	n   int
	sum time.Duration
}

// Phase starts timing a named phase; call the result when it ends.
func Phase(name string) func() {
	if !enabled {
		return func() {}
	}
	t := time.Now()
	return func() {
		mu.Lock()
		defer mu.Unlock()
		phases = append(phases, phase{name, time.Since(t)})
	}
}

// Spawn counts one subprocess of the given kind and sums its time; call the
// result when it exits. Safe for concurrent use.
func Spawn(kind string) func() {
	if !enabled {
		return func() {}
	}
	t := time.Now()
	return func() {
		mu.Lock()
		defer mu.Unlock()
		s := spawns[kind]
		if s == nil {
			s = &spawn{}
			spawns[kind] = s
		}
		s.n++
		s.sum += time.Since(t)
	}
}

// Report writes the phases, the subprocess counts and the total to w. It
// writes nothing when timing is off.
func Report(w io.Writer) {
	if !enabled {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	fmt.Fprintln(w, "timing:")
	for _, p := range phases {
		fmt.Fprintf(w, "  %-22s %8.1f ms\n", p.name, ms(p.d))
	}
	kinds := make([]string, 0, len(spawns))
	for k := range spawns {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		s := spawns[k]
		fmt.Fprintf(w, "  %-22s %4d started, %8.1f ms summed\n", k, s.n, ms(s.sum))
	}
	fmt.Fprintf(w, "  %-22s %8.1f ms\n", "total", ms(time.Since(start)))
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
