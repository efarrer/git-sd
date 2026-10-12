package timing

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func reset(on bool) {
	enabled = on
	tracePath = ""
	phases = nil
	spawns = map[string]*spawn{}
}

func TestOffIsSilent(t *testing.T) {
	reset(false)
	Phase("x")()
	Spawn("gh")()
	var b bytes.Buffer
	Report(&b)
	if b.Len() != 0 || len(phases) != 0 || len(spawns) != 0 {
		t.Errorf("timing off must record and print nothing, got %q", b.String())
	}
}

func TestReportListsPhasesAndSpawns(t *testing.T) {
	reset(true)
	defer reset(false)
	Phase("load metadata")()
	for range 3 {
		Spawn("gh")()
	}
	Spawn("git interpret-trailers")()
	var b bytes.Buffer
	Report(&b)
	out := b.String()
	for _, want := range []string{"timing:", "load metadata", "gh ", "3 started", "git interpret-trailers", "1 started", "total"} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}
	if strings.Index(out, "load metadata") > strings.Index(out, "gh ") {
		t.Errorf("phases come before subprocess counts:\n%s", out)
	}
}

func TestParseTrace2CountsTopLevelGitProcessesByCommand(t *testing.T) {
	trace := `{"event":"version","sid":"a-1","evt":"3"}
{"event":"cmd_name","sid":"a-1","name":"log","hierarchy":"log"}
{"event":"exit","sid":"a-1","t_abs":0.010,"code":0}
{"event":"cmd_name","sid":"b-2","name":"log","hierarchy":"log"}
{"event":"exit","sid":"b-2","t_abs":0.030,"code":0}
{"event":"cmd_name","sid":"c-3","name":"push","hierarchy":"push"}
{"event":"cmd_name","sid":"c-3/d-4","name":"pack-objects","hierarchy":"push/pack-objects"}
{"event":"exit","sid":"c-3/d-4","t_abs":0.500,"code":0}
{"event":"exit","sid":"c-3","t_abs":0.900,"code":0}
{"event":"cmd_name","sid":"e-5","name":"fetch","hierarchy":"fetch"}
not json at all

{"event":"exit","sid":"x-9","t_abs":1.0}
`
	got := parseTrace2(strings.NewReader(trace))
	if g := got["log"]; g == nil || g.n != 2 || g.sum != 40*time.Millisecond {
		t.Errorf("log: %+v, want 2 processes summing 40ms", g)
	}
	if g := got["push"]; g == nil || g.n != 1 || g.sum != 900*time.Millisecond {
		t.Errorf("push: %+v, want 1 process of 900ms", g)
	}
	if got["pack-objects"] != nil {
		t.Errorf("a git process started by git is not top-level: %+v", got["pack-objects"])
	}
	if got["fetch"] != nil {
		t.Errorf("a process with no exit event is not counted: %+v", got["fetch"])
	}
	if len(got) != 2 {
		t.Errorf("got %d commands, want 2: %+v", len(got), got)
	}
	if len(parseTrace2(strings.NewReader(""))) != 0 {
		t.Errorf("an empty trace has no processes")
	}
}
