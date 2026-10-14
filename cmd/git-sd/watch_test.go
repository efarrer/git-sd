package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/efarrer/git-sd/internal/ui"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParseWatchDuration(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"", 7 * time.Second, false},
		{"  ", 7 * time.Second, false},
		{"5", 5 * time.Second, false},
		{"5s", 5 * time.Second, false},
		{"300ms", 300 * time.Millisecond, false},
		{"2m", 2 * time.Minute, false},
		{"0", 0, true},
		{"-3", 0, true},
		{"-3s", 0, true},
		{"soon", 0, true},
	}
	for _, c := range cases {
		got, err := parseWatchDuration("sd.watch.min-interval", c.in, 7*time.Second)
		if (err != nil) != c.wantErr || (!c.wantErr && got != c.want) {
			t.Errorf("parseWatchDuration(%q) = %v, %v; want %v (error %v)", c.in, got, err, c.want, c.wantErr)
		}
	}
}

func TestWatchIntervalsValidation(t *testing.T) {
	if minI, maxI, err := watchIntervals("", ""); err != nil || minI != 5*time.Second || maxI != 60*time.Second {
		t.Errorf("defaults: %v %v %v", minI, maxI, err)
	}
	if _, _, err := watchIntervals("30s", "10s"); err == nil || !strings.Contains(err.Error(), "must not be shorter") {
		t.Errorf("a maximum below the minimum must be rejected, got %v", err)
	}
	if _, _, err := watchIntervals("x", ""); err == nil || !strings.Contains(err.Error(), "sd.watch.min-interval") {
		t.Errorf("an invalid minimum must name its setting, got %v", err)
	}
}

func TestNextWatchInterval(t *testing.T) {
	const s = time.Second
	cases := []struct {
		name               string
		current            time.Duration
		changed, ciRunning bool
		want               time.Duration
	}{
		{"a change resets to the minimum", 40 * s, true, false, 5 * s},
		{"idle doubles", 5 * s, false, false, 10 * s},
		{"idle keeps doubling", 10 * s, false, false, 20 * s},
		{"idle stops at the maximum", 40 * s, false, false, 60 * s},
		{"already at the maximum", 60 * s, false, false, 60 * s},
		{"CI running is capped at 20s", 10 * s, false, true, 20 * s},
		{"CI running stays at the cap", 20 * s, false, true, 20 * s},
		{"CI running starting from a slow wait drops to the cap", 60 * s, false, true, 20 * s},
		{"a short maximum wins over the CI cap", 1 * s, false, true, 2 * s},
	}
	for _, c := range cases {
		maxI := 60 * s
		if strings.Contains(c.name, "short maximum") {
			maxI = 2 * s
		}
		if got := nextWatchInterval(c.current, c.changed, c.ciRunning, 5*s, maxI); got != c.want {
			t.Errorf("%s: nextWatchInterval = %v, want %v", c.name, got, c.want)
		}
	}
	// With a minimum above the doubled wait, the minimum is the floor.
	if got := nextWatchInterval(1*s, false, false, 5*s, 60*s); got != 5*s {
		t.Errorf("the wait never drops below the minimum, got %v", got)
	}
}

func TestEventKind(t *testing.T) {
	cases := []struct {
		from, to ui.MarkerID
		want     string
	}{
		{ui.Review, ui.CIRunning, "ci-running"},
		{ui.CIRunning, ui.CIFailed, "ci-failed"},
		{ui.CIRunning, ui.Review, "ci-passed"},
		{ui.CIRunning, ui.Approved, "ci-passed"},
		{ui.Review, ui.Approved, "approved"},
		{ui.Review, ui.Changes, "changes-requested"},
		{ui.Approved, ui.Conflict, "conflict"},
		{ui.Approved, ui.Merged, "merged"},
		{ui.Review, ui.Closed, "closed"},
		{ui.Review, ui.Draft, "draft"},
		{ui.Draft, ui.Review, "ready-for-review"},
		{ui.Approved, ui.Review, "state-changed"},
		{ui.CIFailed, ui.Review, "state-changed"},
	}
	for _, c := range cases {
		if got := eventKind(c.from, c.to); got != c.want {
			t.Errorf("eventKind(%s -> %s) = %q, want %q", c.from, c.to, got, c.want)
		}
	}
}

func pr(n int, m ui.MarkerID) watchedPR {
	return watchedPR{Number: n, URL: "https://github.com/o/r/pull/" + string(rune('0'+n)), StackSet: "s", Title: "Title " + string(rune('0'+n)), Position: n, SetSize: 3, Marker: m}
}

func TestDiffAndCarryForward(t *testing.T) {
	prev := []watchedPR{pr(1, ui.Review), pr(2, ui.CIRunning), pr(3, ui.Approved)}
	cur := []watchedPR{pr(1, ui.Review), pr(2, ui.Review), pr(3, ""), pr(4, ui.Review)}
	eff := carryForward(prev, cur)
	if eff[2].Marker != ui.Approved {
		t.Errorf("a PR whose lookup failed keeps its last marker, got %q", eff[2].Marker)
	}
	events := diffWatch(prev, eff)
	if len(events) != 1 || events[0].PR.Number != 2 || events[0].Kind != "ci-passed" {
		t.Errorf("only PR 2 changed (CI finished): %+v", events)
	}
	if fresh := newWatchedPRs(prev, eff); len(fresh) != 1 || fresh[0].Number != 4 {
		t.Errorf("PR 4 is new: %+v", fresh)
	}
	// A PR with no previous marker (first sighting that failed) never produces an event.
	if got := diffWatch([]watchedPR{pr(5, "")}, []watchedPR{pr(5, ui.Review)}); len(got) != 0 {
		t.Errorf("an unknown previous state is not a change: %+v", got)
	}
}

// ---------------------------------------------------------------- the loop

type watchStep struct {
	snap watchSnapshot
	err  error
}

type recordingNotifier struct {
	runs [][]string
}

// harness builds a watcher over scripted polls: the loop ends after the last
// poll's sleep, and every sleep is recorded.
func harness(steps []watchStep, tty bool) (w *watcher, out *bytes.Buffer, bell *bytes.Buffer, sleeps *[]time.Duration, rec *recordingNotifier) {
	out, bell, rec = &bytes.Buffer{}, &bytes.Buffer{}, &recordingNotifier{}
	sleeps = &[]time.Duration{}
	i := 0
	clock := time.Date(2026, 10, 10, 15, 0, 0, 0, time.UTC)
	n := &notifier{command: "notify", out: out, run: func(cmd string, env []string) error {
		rec.runs = append(rec.runs, env)
		return nil
	}}
	if tty {
		n.bell = bell
	}
	w = &watcher{
		minInterval: time.Second, maxInterval: 8 * time.Second, out: out, tty: tty,
		snapshot: func() (watchSnapshot, error) {
			s := steps[i]
			i++
			return s.snap, s.err
		},
		sleep: func(ctx context.Context, d time.Duration) bool {
			*sleeps = append(*sleeps, d)
			return i < len(steps)
		},
		now:      func() time.Time { clock = clock.Add(time.Second); return clock },
		notifier: n,
	}
	return
}

// snapOf is a snapshot whose table has one row per PR (the real one is status's).
func snapOf(prs ...watchedPR) watchSnapshot {
	s := watchSnapshot{PRs: prs, Header: []string{"", "Status", "PR"}}
	for _, p := range prs {
		id := p.Marker
		if id == "" {
			id = ui.Unavailable
		}
		mk := ui.MarkerFor(id)
		s.Rows = append(s.Rows, []string{mk.Icon, mk.Word, fmt.Sprintf("#%d", p.Number)})
	}
	return s
}

func TestWatcherReportsChangesAndBacksOff(t *testing.T) {
	w, out, _, sleeps, rec := harness([]watchStep{
		{snap: snapOf(pr(1, ui.Review))},
		{snap: snapOf(pr(1, ui.CIRunning))},
		{snap: snapOf(pr(1, ui.CIRunning))},
		{snap: snapOf(pr(1, ui.CIRunning))},
		{snap: snapOf(pr(1, ui.Approved))},
	}, false)
	if err := w.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "#1") || !strings.Contains(text, "Review") {
		t.Errorf("the baseline table should list the PR:\n%s", text)
	}
	if strings.Count(text, "→") != 2 || !strings.Contains(text, "s 1/3  PR #1  Review → CI running  Title 1") || !strings.Contains(text, "s 1/3  PR #1  CI running → Approved  Title 1") {
		t.Errorf("expected exactly the two transitions as event lines:\n%s", text)
	}
	if !strings.Contains(text, "15:00:") || !strings.HasSuffix(text, "Stopped watching.\n") {
		t.Errorf("event lines carry a time and the run ends with a stop line:\n%s", text)
	}
	want := []time.Duration{time.Second, time.Second, 2 * time.Second, 4 * time.Second, time.Second}
	if !slices.Equal(*sleeps, want) {
		t.Errorf("waits = %v, want %v (min after the baseline and every change, doubling while quiet)", *sleeps, want)
	}
	if len(rec.runs) != 2 {
		t.Fatalf("the command runs once per change, got %d runs", len(rec.runs))
	}
	joined := strings.Join(rec.runs[1], "\n")
	for _, w := range []string{"GIT_SD_EVENT=ci-passed", "GIT_SD_PR_NUMBER=1", "GIT_SD_FROM=CI running", "GIT_SD_TO=Approved", "GIT_SD_PR_TITLE=Title 1", "GIT_SD_STACK_SET=s",
		"GIT_SD_POSITION=1", "GIT_SD_SET_SIZE=3", "GIT_SD_TITLE=s 1/3: CI passed", "GIT_SD_MESSAGE=CI passed on PR 1 of 3 in s (#1), now Approved: Title 1"} {
		if !strings.Contains(joined, w) {
			t.Errorf("the notification environment should contain %q:\n%s", w, joined)
		}
	}
}

func TestEventMessagesIdentifyThePR(t *testing.T) {
	third := watchedPR{Number: 120, StackSet: "feat-a", Title: "Add retry logic", Position: 3, SetSize: 5}
	only := watchedPR{Number: 7, StackSet: "fix-x", Title: "Fix the thing", Position: 1, SetSize: 1}
	ev := func(p watchedPR, from, to ui.MarkerID) watchEvent {
		return watchEvent{PR: p, From: from, To: to, Kind: eventKind(from, to)}
	}
	cases := []struct {
		name           string
		e              watchEvent
		title, message string
	}{
		{"approved", ev(third, ui.Review, ui.Approved), "feat-a 3/5: Approved", "PR 3 of 5 in feat-a (#120) was approved: Add retry logic"},
		{"CI passed", ev(third, ui.CIRunning, ui.Review), "feat-a 3/5: CI passed", "CI passed on PR 3 of 5 in feat-a (#120), now Review: Add retry logic"},
		{"CI failed", ev(third, ui.CIRunning, ui.CIFailed), "feat-a 3/5: CI failed", "CI failed on PR 3 of 5 in feat-a (#120): Add retry logic"},
		{"CI started", ev(third, ui.Review, ui.CIRunning), "feat-a 3/5: CI started", "CI started on PR 3 of 5 in feat-a (#120): Add retry logic"},
		{"changes requested", ev(third, ui.Review, ui.Changes), "feat-a 3/5: Changes requested", "Changes requested on PR 3 of 5 in feat-a (#120): Add retry logic"},
		{"conflict", ev(third, ui.Approved, ui.Conflict), "feat-a 3/5: Merge conflict", "PR 3 of 5 in feat-a (#120) has a merge conflict: Add retry logic"},
		{"merged", ev(third, ui.Approved, ui.Merged), "feat-a 3/5: Merged", "PR 3 of 5 in feat-a (#120) was merged: Add retry logic"},
		{"a set with one PR", ev(only, ui.Review, ui.Approved), "fix-x: Approved", "The PR in fix-x (#7) was approved: Fix the thing"},
		{"anything else", ev(third, ui.Approved, ui.Review), "feat-a 3/5: Review", "PR 3 of 5 in feat-a (#120) changed from Approved to Review: Add retry logic"},
	}
	for _, c := range cases {
		if got := eventTitle(c.e); got != c.title {
			t.Errorf("%s: title %q, want %q", c.name, got, c.title)
		}
		if got := eventMessage(c.e); got != c.message {
			t.Errorf("%s: message %q, want %q", c.name, got, c.message)
		}
	}
}

func TestWatcherOpensWithTheTableStatusPrints(t *testing.T) {
	first := snapOf(pr(1, ui.Review), pr(2, ui.CIRunning))
	w, out, _, _, _ := harness([]watchStep{{snap: first}}, false)
	if err := w.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := ui.FormatTable(first.Header, first.Rows); !strings.HasPrefix(out.String(), want) {
		t.Errorf("the baseline must be exactly the table status prints:\n--- want ---\n%s\n--- got ---\n%s", want, out.String())
	}

	// With no commits in range, status prints a note instead of a table; so does watch.
	empty := watchSnapshot{Note: "No local commits found since origin/main."}
	w, out, _, _, _ = harness([]watchStep{{snap: empty}}, false)
	if err := w.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "No local commits found since origin/main.\n") {
		t.Errorf("a branch with no commits shows status's note:\n%s", out.String())
	}

	// On a terminal the same table is what is redrawn.
	w, out, _, _, _ = harness([]watchStep{{snap: first}}, true)
	if err := w.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), ui.FormatTable(first.Header, first.Rows)) {
		t.Errorf("the screen shows status's table:\n%q", out.String())
	}
}

func TestWatcherCICeilingAndNewPRs(t *testing.T) {
	w, out, _, sleeps, rec := harness([]watchStep{
		{snap: snapOf()},
		{snap: snapOf(pr(1, ui.CIRunning))},
		{snap: snapOf(pr(1, ui.CIRunning))},
		{snap: snapOf(pr(1, ui.CIRunning))},
		{snap: snapOf(pr(1, ui.CIRunning))},
	}, false)
	w.maxInterval = 60 * time.Second
	w.minInterval = 5 * time.Second
	if err := w.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "No PRs to watch yet") || !strings.Contains(text, "Now watching PR #1") {
		t.Errorf("an empty start and a new PR are both announced:\n%s", text)
	}
	if len(rec.runs) != 0 {
		t.Errorf("announcing a new PR is not a change worth notifying: %v", rec.runs)
	}
	want := []time.Duration{5 * time.Second, 5 * time.Second, 10 * time.Second, 20 * time.Second, 20 * time.Second}
	if !slices.Equal(*sleeps, want) {
		t.Errorf("waits = %v, want %v (CI running caps the wait at 20s)", *sleeps, want)
	}
}

func TestWatcherUnreadablePRIsNotAChange(t *testing.T) {
	w, out, _, _, rec := harness([]watchStep{
		{snap: snapOf(pr(1, ui.Approved))},
		{snap: snapOf(pr(1, ""))}, // lookup failed for this PR only
		{snap: snapOf(pr(1, ui.Approved))},
	}, false)
	if err := w.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "→") || len(rec.runs) != 0 {
		t.Errorf("a failed lookup must not create events:\n%s", out.String())
	}
}

func TestWatcherOutageAndRecovery(t *testing.T) {
	boom := errors.New("gh: HTTP 502 Bad Gateway")
	w, out, _, sleeps, rec := harness([]watchStep{
		{snap: snapOf(pr(1, ui.Review))},
		{err: boom},
		{err: boom},
		{err: boom},
		{err: boom},
		{snap: watchSnapshot{PRs: []watchedPR{pr(1, "")}, allFailed: true, err: boom}},
		{snap: snapOf(pr(1, ui.Review))},
	}, false)
	if err := w.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if strings.Count(text, "Cannot reach GitHub") != 1 || !strings.Contains(text, "HTTP 502") {
		t.Errorf("the outage is announced exactly once, with the reason:\n%s", text)
	}
	if strings.Count(text, "Back online.") != 1 {
		t.Errorf("recovery is announced once:\n%s", text)
	}
	if len(rec.runs) != 1 || !strings.Contains(strings.Join(rec.runs[0], "\n"), "GIT_SD_EVENT=offline") {
		t.Errorf("one offline notification: %v", rec.runs)
	}
	for _, d := range *sleeps {
		if d > w.maxInterval {
			t.Errorf("waits must not exceed the maximum, got %v", d)
		}
	}
}

func TestWatcherFirstPollErrorIsFatal(t *testing.T) {
	w, _, _, _, _ := harness([]watchStep{{err: errors.New("gh is not logged in")}}, false)
	if err := w.run(context.Background()); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("an error before any baseline is returned, got %v", err)
	}
}

func TestWatcherOnATerminalRedrawsAndRingsTheBell(t *testing.T) {
	w, out, bell, _, _ := harness([]watchStep{
		{snap: snapOf(pr(1, ui.Review))},
		{snap: snapOf(pr(1, ui.CIFailed))},
	}, true)
	if err := w.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if strings.Count(text, "\x1b[H\x1b[2J") != 2 {
		t.Errorf("each cycle redraws the screen:\n%q", text)
	}
	if !strings.Contains(text, "watching 1 PR(s) · checked 15:00:") || !strings.Contains(text, "next check in") || !strings.Contains(text, "Ctrl-C to stop") {
		t.Errorf("the footer says what is going on:\n%s", text)
	}
	if strings.Contains(text, "Review → CI failed") || strings.Contains(text, "PR #1") {
		t.Errorf("the screen is status's table and a footer, with no history of changes:\n%s", text)
	}
	if !strings.Contains(text, ui.FormatTable(snapOf(pr(1, ui.CIFailed)).Header, snapOf(pr(1, ui.CIFailed)).Rows)) {
		t.Errorf("the latest table is on screen:\n%s", text)
	}
	if bell.String() != "\a" {
		t.Errorf("the bell rings once for the one cycle with a change, got %q", bell.String())
	}
}

func TestNotifierRunsRealCommandsAndWarnsOnce(t *testing.T) {
	file := filepath.Join(t.TempDir(), "got")
	out := &bytes.Buffer{}
	n := &notifier{command: `printf '%s|%s' "$GIT_SD_EVENT" "$GIT_SD_PR_NUMBER" >> ` + file, out: out, run: runNotifyCommand}
	n.notify([]watchEvent{{PR: pr(7, ui.CIFailed), From: ui.CIRunning, To: ui.CIFailed, Kind: "ci-failed"}})
	data, _ := os.ReadFile(file)
	if string(data) != "ci-failed|7" {
		t.Errorf("the command should see the event environment, got %q", data)
	}
	bad := &notifier{command: "exit 3", out: out, run: runNotifyCommand}
	e := []watchEvent{{PR: pr(1, ui.Review), From: ui.Approved, To: ui.Review, Kind: "state-changed"}}
	bad.notify(e)
	bad.notify(e)
	if strings.Count(out.String(), "Warning: sd.watch.notify-command failed") != 1 {
		t.Errorf("a failing command is reported once:\n%s", out.String())
	}
}

func TestSleepWallStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if sleepWall(ctx, time.Minute, nil, nil) {
		t.Error("a cancelled sleep reports false")
	}
	if !sleepWall(context.Background(), 20*time.Millisecond, nil, nil) {
		t.Error("a completed sleep reports true")
	}
}

func TestFooterLine(t *testing.T) {
	state := "watching 2 PR(s) · checked 09:51:18"
	for _, c := range []struct {
		remaining time.Duration
		want      string
	}{
		{20 * time.Second, "next check in 20s"},
		{19400 * time.Millisecond, "next check in 19s"},
		{time.Minute, "next check in 1m0s"},
		{300 * time.Millisecond, "checking now"},
		{0, "checking now"},
		{-time.Second, "checking now"},
	} {
		want := state + " · " + c.want + " · Ctrl-C to stop"
		if got := footerLine(state, c.remaining); got != want {
			t.Errorf("footerLine(%v) = %q, want %q", c.remaining, got, want)
		}
	}
}

func TestCountdownRewritesOnlyTheFooterLine(t *testing.T) {
	var buf bytes.Buffer
	w := &watcher{out: &buf, tty: true, footer: "watching 1 PR(s) · checked 15:00:01"}
	w.countdown(3 * time.Second)
	w.countdown(2 * time.Second)
	want := "\r\x1b[Kwatching 1 PR(s) · checked 15:00:01 · next check in 3s · Ctrl-C to stop" +
		"\r\x1b[Kwatching 1 PR(s) · checked 15:00:01 · next check in 2s · Ctrl-C to stop"
	if buf.String() != want {
		t.Errorf("got %q, want %q", buf.String(), want)
	}
	buf.Reset()
	(&watcher{out: &buf, tty: false, footer: "x"}).countdown(time.Second)
	(&watcher{out: &buf, tty: true}).countdown(time.Second)
	if buf.Len() != 0 {
		t.Errorf("with no terminal or no footer nothing is written, got %q", buf.String())
	}
}

func TestTerminalFooterCountsDownAndStopStartsANewLine(t *testing.T) {
	w, out, _, _, _ := harness([]watchStep{{snap: snapOf(pr(1, ui.Review))}}, true)
	w.sleep = func(ctx context.Context, d time.Duration) bool {
		for _, r := range []time.Duration{9 * time.Second, 8 * time.Second, 7 * time.Second} {
			w.countdown(r)
		}
		return false
	}
	if err := w.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	drawn := strings.Index(text, "next check in 1s") // the full redraw announces the first wait
	rewrite := strings.Index(text, "\r\x1b[Kwatching 1 PR(s) · checked ")
	n9, n8, n7 := strings.Index(text, "next check in 9s"), strings.Index(text, "next check in 8s"), strings.Index(text, "next check in 7s")
	if drawn < 0 || rewrite < drawn || n9 < rewrite || n8 < n9 || n7 < n8 {
		t.Errorf("the footer is drawn once, then rewritten counting down 9s, 8s, 7s in order:\n%q", text)
	}
	if !strings.HasSuffix(text, "next check in 7s · Ctrl-C to stop\nStopped watching.\n") {
		t.Errorf("the stop message must start its own line:\n%q", text)
	}
}

func TestNonTerminalOutputHasNoScreenControl(t *testing.T) {
	w, out, _, _, _ := harness([]watchStep{
		{snap: snapOf(pr(1, ui.Review))},
		{snap: snapOf(pr(1, ui.Approved))},
	}, false)
	if err := w.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(out.String(), "\r\x1b") {
		t.Errorf("piped output must have no carriage returns or escapes:\n%q", out.String())
	}
}

func TestSleepWallTicksEverySecondWithTheTimeLeft(t *testing.T) {
	var left []time.Duration
	start := time.Now()
	if !sleepWall(context.Background(), 2200*time.Millisecond, nil, func(r time.Duration) { left = append(left, r) }) {
		t.Fatalf("an uninterrupted wait returns true")
	}
	if time.Since(start) < 2*time.Second {
		t.Errorf("it waits the full time, took %v", time.Since(start))
	}
	if len(left) < 3 || left[0] > 2200*time.Millisecond || left[0] < 2*time.Second {
		t.Fatalf("the first tick comes before waiting and shows about the whole wait, got %v", left)
	}
	for i := 1; i < len(left); i++ {
		if left[i] >= left[i-1] {
			t.Errorf("the time left must keep decreasing, got %v", left)
		}
	}
	if left[len(left)-1] != 0 {
		t.Errorf("the countdown must reach 0 when the wait ends, got %v", left)
	}
}

// ------------------------------------------------ PRs that leave the view

// lookups returns a stub for watcher.lookup that answers from a map and records who was asked.
func lookups(answers map[int]ui.MarkerID, asked *[]int) func(int) (ui.MarkerID, error) {
	return func(n int) (ui.MarkerID, error) {
		*asked = append(*asked, n)
		if m, ok := answers[n]; ok {
			return m, nil
		}
		return "", errors.New("lookup failed")
	}
}

func TestVanishedPRThatWasMergedIsAnnouncedFromTheLastKnownState(t *testing.T) {
	w, out, _, _, rec := harness([]watchStep{
		{snap: snapOf(pr(1, ui.Approved), pr(2, ui.Review))},
		{snap: snapOf(pr(1, ui.Approved))}, // PR 2's commit was dropped by a rebase
	}, false)
	var asked []int
	w.lookup = lookups(map[int]ui.MarkerID{2: ui.Merged}, &asked)
	if err := w.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "s 2/3  PR #2  Review → Merged  Title 2") || len(rec.runs) != 1 {
		t.Errorf("the merge the watch never saw is announced once, like any change:\n%s\n%v", out.String(), rec.runs)
	}
	if got := strings.Count(out.String(), "→"); got != 1 {
		t.Errorf("only the merge is announced, not the removal, got %d changes:\n%s", got, out.String())
	}
	if len(asked) != 1 || asked[0] != 2 {
		t.Errorf("only the vanished PR is looked up, got %v", asked)
	}
}

func TestVanishedPRAlreadyKnownAsMergedNeedsNoLookup(t *testing.T) {
	for _, last := range []ui.MarkerID{ui.Merged, ui.Closed} {
		w, out, _, _, rec := harness([]watchStep{
			{snap: snapOf(pr(1, ui.Approved), pr(2, last))},
			{snap: snapOf(pr(1, ui.Approved))},
		}, false)
		var asked []int
		w.lookup = lookups(map[int]ui.MarkerID{2: ui.Merged}, &asked)
		if err := w.run(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(asked) != 0 || strings.Contains(out.String(), "→") || len(rec.runs) != 0 {
			t.Errorf("a PR already announced as %s leaves quietly: asked %v\n%s", last, asked, out.String())
		}
	}
}

func TestVanishedPRThatIsStillOpenOrUnknownIsSilent(t *testing.T) {
	for name, answers := range map[string]map[int]ui.MarkerID{
		"still open":        {2: ui.Review},
		"lookup failed":     {},
		"changes requested": {2: ui.Changes},
	} {
		w, out, _, _, rec := harness([]watchStep{
			{snap: snapOf(pr(1, ui.Approved), pr(2, ui.Review))},
			{snap: snapOf(pr(1, ui.Approved))},
		}, false)
		var asked []int
		w.lookup = lookups(answers, &asked)
		if err := w.run(context.Background()); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), "→") || len(rec.runs) != 0 {
			t.Errorf("%s: a PR that merely left the branch is dropped silently:\n%s", name, out.String())
		}
	}
}

func TestVanishedPRThatWasClosedIsAnnounced(t *testing.T) {
	w, out, _, _, _ := harness([]watchStep{
		{snap: snapOf(pr(1, ui.Review), pr(2, ui.Draft))},
		{snap: snapOf(pr(1, ui.Review))},
	}, false)
	var asked []int
	w.lookup = lookups(map[int]ui.MarkerID{2: ui.Closed}, &asked)
	if err := w.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "PR #2  Draft → Closed") {
		t.Errorf("a closed PR is announced:\n%s", out.String())
	}
}

func TestSeveralVanishedPRsAreLookedUpInStackOrder(t *testing.T) {
	w, out, _, _, rec := harness([]watchStep{
		{snap: snapOf(pr(1, ui.Approved), pr(2, ui.Approved), pr(3, ui.Review))},
		{snap: snapOf(pr(3, ui.Review))},
	}, false)
	var asked []int
	w.lookup = lookups(map[int]ui.MarkerID{1: ui.Merged, 2: ui.Merged}, &asked)
	if err := w.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 2 || asked[0] != 1 || asked[1] != 2 || len(rec.runs) != 2 {
		t.Errorf("both are looked up in stack order and announced: asked %v, notified %d\n%s", asked, len(rec.runs), out.String())
	}
}

func TestBaselinePollAndPresentPRsNeverTriggerLookups(t *testing.T) {
	w, _, _, _, _ := harness([]watchStep{
		{snap: snapOf(pr(1, ui.Review), pr(2, ui.Review))},
		{snap: snapOf(pr(1, ui.Approved), pr(2, ui.Review))},
	}, false)
	var asked []int
	w.lookup = lookups(map[int]ui.MarkerID{}, &asked)
	if err := w.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 0 {
		t.Errorf("nothing left the view, so nothing is looked up, got %v", asked)
	}
}

// While sync or merge has the branch rearranged the view can be empty: that must
// not look like every PR leaving, nor like every PR being new when it ends.
func TestUnsettledPollIsIgnored(t *testing.T) {
	w, out, _, _, rec := harness([]watchStep{
		{snap: snapOf(pr(1, ui.Approved), pr(2, ui.Review))},
		{snap: watchSnapshot{Note: "No local commits found since origin/main.", unsettled: true}},
		{snap: snapOf(pr(1, ui.Approved), pr(2, ui.Review))},
	}, false)
	var asked []int
	w.lookup = lookups(map[int]ui.MarkerID{1: ui.Merged, 2: ui.Merged}, &asked)
	if err := w.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 0 || strings.Contains(out.String(), "Now watching") || strings.Contains(out.String(), "→") || len(rec.runs) != 0 {
		t.Errorf("an unsettled poll teaches the watch nothing: asked %v\n%s", asked, out.String())
	}
}
