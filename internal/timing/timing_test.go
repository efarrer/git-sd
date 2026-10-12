package timing

import (
	"bytes"
	"strings"
	"testing"
)

func reset(on bool) {
	enabled = on
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
