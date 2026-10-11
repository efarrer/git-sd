package github

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCIStatus(t *testing.T) {
	run := func(status, conclusion string) string {
		return `{"__typename":"CheckRun","name":"build","status":"` + status + `","conclusion":"` + conclusion + `","workflowName":"CI","detailsUrl":"https://x"}`
	}
	ctx := func(state string) string {
		return `{"__typename":"StatusContext","context":"ci/legacy","state":"` + state + `","targetUrl":"https://x"}`
	}
	list := func(entries ...string) json.RawMessage {
		out := "["
		for i, e := range entries {
			if i > 0 {
				out += ","
			}
			out += e
		}
		return json.RawMessage(out + "]")
	}
	cases := []struct {
		name   string
		rollup json.RawMessage
		want   CIStatus
	}{
		{"no rollup at all", nil, CINone},
		{"an empty rollup (no checks)", list(), CINone},
		{"null", json.RawMessage("null"), CINone},
		{"all green", list(run("COMPLETED", "SUCCESS"), ctx("SUCCESS")), CINone},
		{"a failed check run", list(run("COMPLETED", "SUCCESS"), run("COMPLETED", "FAILURE")), CIFailing},
		{"timed out", list(run("COMPLETED", "TIMED_OUT")), CIFailing},
		{"failed to start", list(run("COMPLETED", "STARTUP_FAILURE")), CIFailing},
		{"action required", list(run("COMPLETED", "ACTION_REQUIRED")), CIFailing},
		{"a failed commit status", list(ctx("FAILURE")), CIFailing},
		{"an errored commit status", list(ctx("ERROR")), CIFailing},
		{"queued", list(run("QUEUED", "")), CIRunning},
		{"in progress", list(run("IN_PROGRESS", "")), CIRunning},
		{"waiting at an approval gate is not running", list(run("WAITING", "")), CINone},
		{"a gate waiting beside finished checks", list(run("COMPLETED", "SUCCESS"), run("WAITING", ""), run("COMPLETED", "SKIPPED")), CINone},
		{"a gate does not hide a check still running", list(run("WAITING", ""), run("IN_PROGRESS", "")), CIRunning},
		{"a gate does not hide a failure", list(run("WAITING", ""), run("COMPLETED", "FAILURE")), CIFailing},
		{"pending", list(run("PENDING", "")), CIRunning},
		{"a pending commit status", list(ctx("PENDING")), CIRunning},
		{"an expected commit status", list(ctx("EXPECTED")), CIRunning},
		{"failing wins over running", list(run("IN_PROGRESS", ""), run("COMPLETED", "FAILURE")), CIFailing},
		{"failing wins over running, either order", list(ctx("FAILURE"), run("QUEUED", "")), CIFailing},
		{"running wins over success", list(run("COMPLETED", "SUCCESS"), run("IN_PROGRESS", "")), CIRunning},
		{"cancelled counts as neither", list(run("COMPLETED", "CANCELLED")), CINone},
		{"skipped counts as neither", list(run("COMPLETED", "SKIPPED"), run("COMPLETED", "SUCCESS")), CINone},
		{"neutral counts as neither", list(run("COMPLETED", "NEUTRAL")), CINone},
		{"stale counts as neither", list(run("COMPLETED", "STALE")), CINone},
		{"an unknown entry type is ignored", list(`{"__typename":"Something","state":"FAILURE"}`), CINone},
		{"a rollup that can't be read is no information", json.RawMessage(`{"not":"a list"}`), CINone},
		{"garbage", json.RawMessage(`[`), CINone},
	}
	for _, c := range cases {
		got := (&DetailedPRInfo{StatusCheckRollup: c.rollup}).CIStatus()
		if got != c.want {
			t.Errorf("%s: CIStatus = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestCIProgress(t *testing.T) {
	run := func(status, conclusion string) string {
		return `{"__typename":"CheckRun","name":"build","status":"` + status + `","conclusion":"` + conclusion + `"}`
	}
	ctx := func(state string) string {
		return `{"__typename":"StatusContext","context":"ci/legacy","state":"` + state + `"}`
	}
	list := func(entries ...string) json.RawMessage {
		return json.RawMessage("[" + strings.Join(entries, ",") + "]")
	}
	cases := []struct {
		name                    string
		rollup                  json.RawMessage
		finished, failed, total int
	}{
		{"no rollup", nil, 0, 0, 0},
		{"null", json.RawMessage("null"), 0, 0, 0},
		{"unreadable", json.RawMessage("{"), 0, 0, 0},
		{"empty", list(), 0, 0, 0},
		{"all done", list(run("COMPLETED", "SUCCESS"), ctx("SUCCESS")), 2, 0, 2},
		{"mid-run", list(run("COMPLETED", "SUCCESS"), run("IN_PROGRESS", ""), run("QUEUED", "")), 1, 0, 3},
		{"a failure", list(run("COMPLETED", "FAILURE"), run("COMPLETED", "SUCCESS"), run("IN_PROGRESS", "")), 2, 1, 3},
		{"skipped, neutral, cancelled and stale count as finished", list(run("COMPLETED", "SKIPPED"), run("COMPLETED", "NEUTRAL"), run("COMPLETED", "CANCELLED"), run("COMPLETED", "STALE")), 4, 0, 4},
		{"a waiting gate is left out", list(run("COMPLETED", "SUCCESS"), run("WAITING", ""), run("IN_PROGRESS", "")), 1, 0, 2},
		{"only a gate left", list(run("WAITING", "")), 0, 0, 0},
		{"commit statuses", list(ctx("PENDING"), ctx("EXPECTED"), ctx("SUCCESS"), ctx("FAILURE"), ctx("ERROR")), 3, 2, 5},
		{"timed out and action required are failures", list(run("COMPLETED", "TIMED_OUT"), run("COMPLETED", "ACTION_REQUIRED"), run("COMPLETED", "STARTUP_FAILURE")), 3, 3, 3},
		{"same name twice counts twice", list(run("COMPLETED", "SUCCESS"), run("COMPLETED", "SUCCESS")), 2, 0, 2},
		{"an unknown entry type is not counted", list(`{"__typename":"Other"}`, run("COMPLETED", "SUCCESS")), 1, 0, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := &DetailedPRInfo{StatusCheckRollup: c.rollup}
			f, x, n := d.CIProgress()
			if f != c.finished || x != c.failed || n != c.total {
				t.Errorf("CIProgress() = finished %d, failed %d, total %d; want %d, %d, %d", f, x, n, c.finished, c.failed, c.total)
			}
		})
	}
}
