// Package timing is the GIT_SD_TIMING=1 diagnostic: it records how long the
// phases of a command take and how many subprocesses it starts, and prints
// that to stderr. Everything is a no-op unless the variable is set to 1.
package timing

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	enabled = os.Getenv("GIT_SD_TIMING") == "1"
	start   = time.Now()

	mu     sync.Mutex
	phases []phase
	spawns = map[string]*spawn{}

	// tracePath is the trace2 file git writes to (see Report); empty when off
	// or when it couldn't be created.
	tracePath string
)

func init() {
	if !enabled {
		return
	}
	// Every git process started from here on, by us or by gh, appends its
	// start and exit to this file; Report counts them.
	if f, err := os.CreateTemp("", "git-sd-trace2-*.json"); err == nil {
		f.Close()
		tracePath = f.Name()
		os.Setenv("GIT_TRACE2_EVENT", tracePath)
	}
}

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

// parseTrace2 reads git's trace2 event log and returns, per git command name
// ("rev-parse", "log", ...), how many top-level processes ran and their summed
// elapsed time. A process is top-level when its session id has no "/" (git
// appends "/<its own id>" for the git commands that git itself starts), and
// is counted once it has both named itself and exited. Lines that aren't JSON
// events are skipped.
func parseTrace2(r io.Reader) map[string]*spawn {
	type event struct {
		Event string  `json:"event"`
		Sid   string  `json:"sid"`
		Name  string  `json:"name"`
		TAbs  float64 `json:"t_abs"`
	}
	names := map[string]string{}
	elapsed := map[string]float64{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var e event
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.Sid == "" || strings.Contains(e.Sid, "/") {
			continue
		}
		switch e.Event {
		case "cmd_name":
			names[e.Sid] = e.Name
		case "exit":
			elapsed[e.Sid] = e.TAbs
		}
	}
	out := map[string]*spawn{}
	for sid, name := range names {
		secs, done := elapsed[sid]
		if !done {
			continue
		}
		s := out[name]
		if s == nil {
			s = &spawn{}
			out[name] = s
		}
		s.n++
		s.sum += time.Duration(secs * float64(time.Second))
	}
	return out
}

// Report writes the phases, the subprocess counts and the total to w. It
// writes nothing when timing is off. Call it once, as the command finishes.
func Report(w io.Writer) {
	if !enabled {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	if tracePath != "" {
		if f, err := os.Open(tracePath); err == nil {
			for name, s := range parseTrace2(f) {
				spawns["git "+name] = s
			}
			f.Close()
		}
		os.Remove(tracePath)
		tracePath = ""
	}
	fmt.Fprintln(w, "timing:")
	for _, p := range phases {
		fmt.Fprintf(w, "  %-26s %8.1f ms\n", p.name, ms(p.d))
	}
	kinds := make([]string, 0, len(spawns))
	for k := range spawns {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		s := spawns[k]
		fmt.Fprintf(w, "  %-26s %4d started, %8.1f ms summed\n", k, s.n, ms(s.sum))
	}
	fmt.Fprintf(w, "  %-26s %8.1f ms\n", "total", ms(time.Since(start)))
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
