package main

import (
	"context"
	"fmt"
	"github.com/efarrer/git-sd/internal/git"
	"github.com/efarrer/git-sd/internal/github"
	"github.com/efarrer/git-sd/internal/ui"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	defaultWatchMinInterval = 5 * time.Second
	defaultWatchMaxInterval = 60 * time.Second
	// watchCIMaxInterval caps the wait while any PR has CI running: the one
	// state that changes within seconds. Review states are human-paced.
	watchCIMaxInterval = 20 * time.Second
	// watchOutageAfter is how many failed polls in a row count as an outage.
	watchOutageAfter   = 3
	watchNotifyTimeout = 10 * time.Second
)

// watchedPR is one PR the watcher follows, as of one poll.
type watchedPR struct {
	Number   int
	URL      string
	StackSet string
	Title    string
	Position int         // 1-based place among the groups of its Stack Set, in stack order
	SetSize  int         // how many groups that Stack Set has
	Marker   ui.MarkerID // the state GitHub reports; "" if it could not be read
}

// watchSnapshot is one poll of every watched PR.
type watchSnapshot struct {
	Header    []string // the table status would print for this moment
	Rows      [][]string
	Note      string      // printed instead of the table (no commits in range)
	PRs       []watchedPR // in stack order
	allFailed bool        // every lookup failed (GitHub or the network is unreachable)
	err       error       // the first lookup error
}

// watchEvent is a PR whose GitHub-reported state changed between two polls.
type watchEvent struct {
	PR       watchedPR
	From, To ui.MarkerID
	Kind     string
}

// ---------------------------------------------------------------- settings

// parseWatchDuration reads an interval setting: empty is the default, a bare
// number is seconds, anything else is a Go duration ("300ms", "1m").
func parseWatchDuration(name, value string, def time.Duration) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return def, nil
	}
	if n, err := strconv.Atoi(value); err == nil {
		if n > 0 {
			return time.Duration(n) * time.Second, nil
		}
	} else if d, err := time.ParseDuration(value); err == nil && d > 0 {
		return d, nil
	}
	return 0, fmt.Errorf("%s must be a positive number of seconds or a duration such as 5s or 300ms, got '%s'", name, value)
}

// watchIntervals reads and validates sd.watch.min-interval / max-interval.
func watchIntervals(minValue, maxValue string) (minI, maxI time.Duration, err error) {
	if minI, err = parseWatchDuration("sd.watch.min-interval", minValue, defaultWatchMinInterval); err != nil {
		return 0, 0, err
	}
	if maxI, err = parseWatchDuration("sd.watch.max-interval", maxValue, defaultWatchMaxInterval); err != nil {
		return 0, 0, err
	}
	if maxI < minI {
		return 0, 0, fmt.Errorf("sd.watch.max-interval (%s) must not be shorter than sd.watch.min-interval (%s)", maxI, minI)
	}
	return minI, maxI, nil
}

// nextWatchInterval is how long to wait before the next poll: the minimum after
// a change, otherwise double the last wait, up to the maximum (and up to
// watchCIMaxInterval while any PR has CI running).
func nextWatchInterval(current time.Duration, changed, ciRunning bool, minI, maxI time.Duration) time.Duration {
	if changed {
		return minI
	}
	limit := maxI
	if ciRunning && limit > watchCIMaxInterval {
		limit = watchCIMaxInterval
	}
	next := max(current*2, minI)
	return min(next, limit)
}

// holdWatchInterval is the wait after an extra poll that another command asked
// for and that found nothing new: the schedule is left as it was (not doubled,
// not reset), still held under the ceiling that applies while CI is running.
func holdWatchInterval(current time.Duration, ciRunning bool, minI, maxI time.Duration) time.Duration {
	limit := maxI
	if ciRunning && limit > watchCIMaxInterval {
		limit = watchCIMaxInterval
	}
	return min(max(current, minI), limit)
}

// ---------------------------------------------------------------- events

// eventKind names a transition for GIT_SD_EVENT.
func eventKind(from, to ui.MarkerID) string {
	switch to {
	case ui.CIRunning:
		return "ci-running"
	case ui.CIFailed:
		return "ci-failed"
	case ui.Approved:
		if from != ui.CIRunning {
			return "approved"
		}
	case ui.Changes:
		return "changes-requested"
	case ui.Conflict:
		return "conflict"
	case ui.Merged:
		return "merged"
	case ui.Closed:
		return "closed"
	case ui.Draft:
		return "draft"
	case ui.Review:
		if from == ui.Draft {
			return "ready-for-review"
		}
	}
	if from == ui.CIRunning {
		return "ci-passed" // CI finished without failing, and the PR moved on
	}
	return "state-changed"
}

func markerWord(id ui.MarkerID) string {
	if id == "" {
		return "?"
	}
	return ui.MarkerFor(id).Word
}

// setLabel names a PR's place: "feat-a 3/5", or just the set when it has one PR.
func setLabel(p watchedPR) string {
	if p.SetSize > 1 && p.Position > 0 {
		return fmt.Sprintf("%s %d/%d", p.StackSet, p.Position, p.SetSize)
	}
	return p.StackSet
}

// describeEvent is the one-line log entry for a change.
func describeEvent(e watchEvent) string {
	return fmt.Sprintf("%s  PR #%d  %s → %s  %s", setLabel(e.PR), e.PR.Number, markerWord(e.From), markerWord(e.To), e.PR.Title)
}

// who identifies a PR the way a person would say it: "PR 3 of 5 in feat-a
// (#120)", or "the PR in feat-a (#120)" when the set has only one.
func who(p watchedPR) string {
	if p.SetSize > 1 && p.Position > 0 {
		return fmt.Sprintf("PR %d of %d in %s (#%d)", p.Position, p.SetSize, p.StackSet, p.Number)
	}
	return fmt.Sprintf("the PR in %s (#%d)", p.StackSet, p.Number)
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// eventHeadline is what happened, in a few words.
func eventHeadline(e watchEvent) string {
	switch e.Kind {
	case "ci-running":
		return "CI started"
	case "ci-passed":
		return "CI passed"
	case "ci-failed":
		return "CI failed"
	case "approved":
		return "Approved"
	case "changes-requested":
		return "Changes requested"
	case "conflict":
		return "Merge conflict"
	case "merged":
		return "Merged"
	case "closed":
		return "Closed"
	case "draft":
		return "Marked as draft"
	case "ready-for-review":
		return "Ready for review"
	}
	return markerWord(e.To)
}

// eventTitle is a short notification title: the set, the place in it and what
// happened, e.g. "feat-a 3/5: Approved".
func eventTitle(e watchEvent) string {
	return setLabel(e.PR) + ": " + eventHeadline(e)
}

// eventMessage is a full sentence for the notification body that says which PR
// changed and how, e.g. "CI passed on PR 3 of 5 in feat-a (#120), now Approved: <title>".
func eventMessage(e watchEvent) string {
	w, t := who(e.PR), e.PR.Title
	switch e.Kind {
	case "ci-running":
		return fmt.Sprintf("CI started on %s: %s", w, t)
	case "ci-passed":
		return fmt.Sprintf("CI passed on %s, now %s: %s", w, markerWord(e.To), t)
	case "ci-failed":
		return fmt.Sprintf("CI failed on %s: %s", w, t)
	case "approved":
		return fmt.Sprintf("%s was approved: %s", capitalize(w), t)
	case "changes-requested":
		return fmt.Sprintf("Changes requested on %s: %s", w, t)
	case "conflict":
		return fmt.Sprintf("%s has a merge conflict: %s", capitalize(w), t)
	case "merged":
		return fmt.Sprintf("%s was merged: %s", capitalize(w), t)
	case "closed":
		return fmt.Sprintf("%s was closed: %s", capitalize(w), t)
	case "draft":
		return fmt.Sprintf("%s was marked as a draft: %s", capitalize(w), t)
	case "ready-for-review":
		return fmt.Sprintf("%s is ready for review: %s", capitalize(w), t)
	}
	return fmt.Sprintf("%s changed from %s to %s: %s", capitalize(w), markerWord(e.From), markerWord(e.To), t)
}

// carryForward gives a PR whose lookup failed this time (no marker) its last
// known marker, so a failed lookup is never mistaken for a change.
func carryForward(prev, cur []watchedPR) []watchedPR {
	old := map[int]ui.MarkerID{}
	for _, p := range prev {
		old[p.Number] = p.Marker
	}
	out := make([]watchedPR, len(cur))
	for i, p := range cur {
		if p.Marker == "" {
			p.Marker = old[p.Number]
		}
		out[i] = p
	}
	return out
}

// diffWatch returns the PRs present in both snapshots whose marker changed.
func diffWatch(prev, cur []watchedPR) []watchEvent {
	old := map[int]ui.MarkerID{}
	for _, p := range prev {
		old[p.Number] = p.Marker
	}
	var events []watchEvent
	for _, p := range cur {
		from, seen := old[p.Number]
		if !seen || from == "" || p.Marker == "" || from == p.Marker {
			continue
		}
		events = append(events, watchEvent{PR: p, From: from, To: p.Marker, Kind: eventKind(from, p.Marker)})
	}
	return events
}

// newWatchedPRs are the PRs in cur that prev did not have.
func newWatchedPRs(prev, cur []watchedPR) []watchedPR {
	seen := map[int]bool{}
	for _, p := range prev {
		seen[p.Number] = true
	}
	var out []watchedPR
	for _, p := range cur {
		if !seen[p.Number] {
			out = append(out, p)
		}
	}
	return out
}

// ---------------------------------------------------------------- notifying

// notifier rings the bell (terminal only) and runs sd.watch.notify-command.
type notifier struct {
	command string
	bell    io.Writer // nil unless output is a terminal
	out     io.Writer // for the one warning about a failing command
	run     func(command string, env []string) error
	warned  bool
}

func (n *notifier) invoke(env []string) {
	if n.command == "" || n.run == nil {
		return
	}
	if err := n.run(n.command, env); err != nil && !n.warned {
		n.warned = true
		fmt.Fprintf(n.out, "Warning: sd.watch.notify-command failed: %v\n", err)
	}
}

// notify rings the bell once and runs the command once per event.
func (n *notifier) notify(events []watchEvent) {
	if len(events) == 0 {
		return
	}
	if n.bell != nil {
		fmt.Fprint(n.bell, "\a")
	}
	for _, e := range events {
		n.invoke(eventEnv(e))
	}
}

// eventEnv is the environment the notify command gets for one PR change, in
// the order of notifyVariables (a test keeps the two in step).
func eventEnv(e watchEvent) []string {
	return []string{
		"GIT_SD_TITLE=" + eventTitle(e),
		"GIT_SD_MESSAGE=" + eventMessage(e),
		"GIT_SD_EVENT=" + e.Kind,
		"GIT_SD_STACK_SET=" + e.PR.StackSet,
		"GIT_SD_POSITION=" + strconv.Itoa(e.PR.Position),
		"GIT_SD_SET_SIZE=" + strconv.Itoa(e.PR.SetSize),
		"GIT_SD_PR_NUMBER=" + strconv.Itoa(e.PR.Number),
		"GIT_SD_PR_URL=" + e.PR.URL,
		"GIT_SD_PR_TITLE=" + e.PR.Title,
		"GIT_SD_FROM=" + markerWord(e.From),
		"GIT_SD_TO=" + markerWord(e.To),
	}
}

// messageEnv is the environment for a notification that is not about one PR
// (an outage): only the first three variables are set.
func messageEnv(kind, text string) []string {
	return []string{"GIT_SD_TITLE=git sd watch: " + kind, "GIT_SD_MESSAGE=" + text, "GIT_SD_EVENT=" + kind}
}

// message sends a notification that is not about one PR (an outage).
func (n *notifier) message(kind, text string) {
	if n.bell != nil {
		fmt.Fprint(n.bell, "\a")
	}
	n.invoke(messageEnv(kind, text))
}

// runNotifyCommand runs the command through sh with the event environment.
func runNotifyCommand(command string, env []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), watchNotifyTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Env = append(os.Environ(), env...)
	return cmd.Run()
}

// ---------------------------------------------------------------- the loop

type watcher struct {
	minInterval, maxInterval time.Duration
	out                      io.Writer
	tty                      bool
	snapshot                 func() (watchSnapshot, error)
	sleep                    func(ctx context.Context, d time.Duration) bool // false once ctx is done
	footer                   string                                          // the footer without its countdown: what is rewritten each second
	rung                     func() bool                                     // another command asked for a check now (consumes the request)
	now                      func() time.Time
	notifier                 *notifier
}

func (w *watcher) stamp() string { return w.now().Format("15:04:05") }

// render is what status would print for this snapshot: its table, or the
// note when there are no commits.
func (w *watcher) render(snap watchSnapshot) string {
	if snap.Note != "" {
		return snap.Note + "\n"
	}
	return ui.FormatTable(snap.Header, snap.Rows)
}

// run polls until ctx is cancelled. An error on the first poll is returned;
// later errors are retried.
func (w *watcher) run(ctx context.Context) error {
	interval := w.minInterval
	var prev []watchedPR
	var latest watchSnapshot // the last good snapshot: what is on screen
	have := false            // a baseline has been taken
	failures, outage := 0, false
	var lastChecked string
	say := func(line string) {
		line = w.stamp() + "  " + line
		if w.tty {
			return // the screen is status's table and a footer: no history (the bell and notifications carry changes)
		}
		fmt.Fprintln(w.out, line)
	}
	for {
		poked := w.rung != nil && have && w.rung() // another command rang: this poll restarts the cadence
		snap, err := w.snapshot()
		changed := false
		switch {
		case err != nil && !have:
			return err
		case err != nil || snap.allFailed:
			failures++
			if failures == watchOutageAfter && !outage {
				outage = true
				reason := "no answer"
				if err != nil {
					reason = err.Error()
				} else if snap.err != nil {
					reason = snap.err.Error()
				}
				say("Cannot reach GitHub (" + strings.SplitN(reason, "\n", 2)[0] + "); still watching, will keep retrying.")
				w.notifier.message("offline", "git sd watch cannot reach GitHub; it will keep retrying")
			}
		default:
			if outage {
				say("Back online.")
				outage = false
			}
			failures = 0
			cur := carryForward(prev, snap.PRs)
			if !have {
				have, changed = true, true // just started: poll again after the minimum
				if !w.tty {
					fmt.Fprint(w.out, w.render(snap))
				}
				if len(cur) == 0 {
					say("No PRs to watch yet; run 'git sd sync' to create them. Waiting.")
				}
			} else {
				events := diffWatch(prev, cur)
				fresh := newWatchedPRs(prev, cur)
				for _, p := range fresh {
					say(fmt.Sprintf("Now watching PR #%d", p.Number))
				}
				for _, e := range events {
					say(describeEvent(e))
				}
				changed = len(events) > 0 || len(fresh) > 0
				w.notifier.notify(events)
			}
			prev, latest = cur, snap
			lastChecked = w.stamp()
		}
		ciRunning := false
		for _, p := range prev {
			if p.Marker == ui.CIRunning {
				ciRunning = true
			}
		}
		if poked && !changed {
			interval = holdWatchInterval(interval, ciRunning, w.minInterval, w.maxInterval)
		} else {
			interval = nextWatchInterval(interval, changed, ciRunning, w.minInterval, w.maxInterval)
		}
		if w.tty {
			fmt.Fprint(w.out, "\x1b[H\x1b[2J")
			fmt.Fprint(w.out, w.render(latest))
			state := fmt.Sprintf("watching %d PR(s) · checked %s", len(prev), lastChecked)
			if outage {
				state = "GitHub unreachable, retrying · last good check " + lastChecked
			}
			w.footer = state
			fmt.Fprint(w.out, "\n"+footerLine(state, interval)) // no newline: countdown rewrites this line
		}
		if !w.sleep(ctx, interval) {
			if w.tty {
				fmt.Fprintln(w.out) // end the footer line
			}
			fmt.Fprintln(w.out, "Stopped watching.")
			return nil
		}
	}
}

// footerLine is the terminal footer: the state, and the time left to the next
// check (or "checking now" once it is due).
func footerLine(state string, remaining time.Duration) string {
	left := max(remaining, 0).Round(time.Second)
	if left == 0 {
		return state + " · checking now · Ctrl-C to stop" // the poll takes a second or two
	}
	return fmt.Sprintf("%s · next check in %s · Ctrl-C to stop", state, left)
}

// countdown rewrites the footer line in place with the time left. Terminal only.
func (w *watcher) countdown(remaining time.Duration) {
	if w.tty && w.footer != "" {
		fmt.Fprint(w.out, "\r\x1b[K"+footerLine(w.footer, remaining))
	}
}

// sleepWall waits d, checking the wall clock every second so that after a
// laptop wakes the next poll is immediate instead of up to a full interval late.
// wake is asked once a second too; when it says true the wait ends early. tick,
// if not nil, is told the time left each time, first before waiting at all and
// finally with 0 when the wait ends, whether it ran out or was cut short by a ring.
func sleepWall(ctx context.Context, d time.Duration, wake func() bool, tick func(remaining time.Duration)) bool {
	deadline := time.Now().Round(0).Add(d)
	for {
		if wake != nil && wake() {
			if tick != nil {
				tick(0) // rung by another command: the check starts now
			}
			return ctx.Err() == nil
		}
		remaining := time.Until(deadline.Round(0))
		remaining = deadline.Sub(time.Now().Round(0))
		if remaining <= 0 {
			if tick != nil {
				tick(0) // the wait is over: show 0s while the check runs
			}
			return ctx.Err() == nil
		}
		if tick != nil {
			tick(remaining)
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(min(remaining, time.Second)):
		}
	}
}

// ---------------------------------------------------------------- the command

// takeWatchSnapshot builds the same view status prints (read-only: no index
// refresh, no save) and takes the watched PRs from it.
func takeWatchSnapshot(columns []string, color bool) (watchSnapshot, error) {
	view, err := buildStatusView(statusViewOptions{Columns: columns, Color: color, Op: activeOperation()})
	if err != nil {
		return watchSnapshot{}, err // its text is the cause; the "Error:" prefix belongs to the caller
	}
	snap := watchSnapshot{PRs: view.PRs, Header: view.Header, Rows: view.Rows, err: view.FirstLookupErr}
	if view.NoCommits {
		snap.Note = fmt.Sprintf("No local commits found since %s.", view.BaseBranch)
	}
	snap.allFailed = view.LookupsTotal > 0 && view.LookupsFailed == view.LookupsTotal
	if snap.allFailed && github.IsEnvError(snap.err) {
		return snap, snap.err // no usable gh: fatal on the first poll, an outage later
	}
	return snap, nil
}

func runWatch(args []string) {
	if len(args) > 0 {
		fmt.Printf("Error: watch takes no arguments, got '%s'\n", args[0])
		exitCleanly(1)
	}
	columns, err := statusColumns(git.ConfigGetWithDefault("sd.status.columns", defaultStatusColumns))
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		exitCleanly(1)
	}
	minI, maxI, err := watchIntervals(
		git.ConfigGetWithDefault("sd.watch.min-interval", ""),
		git.ConfigGetWithDefault("sd.watch.max-interval", ""))
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		exitCleanly(1)
	}
	release, err := acquireWatch() // one watch per repository; also the doorbell other commands ring
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		exitCleanly(1)
	}
	tty := stdoutIsTerminal()
	n := &notifier{command: strings.TrimSpace(git.ConfigGetWithDefault("sd.watch.notify-command", "")), out: os.Stdout, run: runNotifyCommand}
	if tty {
		n.bell = os.Stdout
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	color := ui.ColorEnabled(git.ConfigGetWithDefault("sd.status.color", "true"), os.Getenv, tty)
	w := &watcher{
		minInterval: minI, maxInterval: maxI, out: os.Stdout, tty: tty,
		snapshot: func() (watchSnapshot, error) { return takeWatchSnapshot(columns, color) },
		now:      time.Now, notifier: n,
	}
	feed := newRingFeed() // other commands ring it by touching watch.pid
	w.rung = feed.take
	var tick func(time.Duration)
	if tty {
		tick = w.countdown
	}
	w.sleep = func(ctx context.Context, d time.Duration) bool { return sleepWall(ctx, d, feed.changed, tick) }
	err = w.run(ctx)
	release()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		exitCleanly(1)
	}
}
