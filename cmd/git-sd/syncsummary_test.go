package main

import (
	"reflect"
	"testing"
)

func TestPRFingerprintChangesWithEachPart(t *testing.T) {
	base := prFingerprint("Title", "Body text", "main")
	if base != prFingerprint("Title", "Body text", "main") {
		t.Fatal("fingerprint is not stable for identical input")
	}
	for name, other := range map[string]string{
		"title": prFingerprint("Title 2", "Body text", "main"),
		"body":  prFingerprint("Title", "Body text.", "main"),
		"base":  prFingerprint("Title", "Body text", "feat-a-s1234"),
	} {
		if other == base {
			t.Fatalf("changing the %s did not change the fingerprint", name)
		}
	}
	// Fields are length-prefixed, so moving a separator between parts can't collide.
	if prFingerprint("a", "b|c", "d") == prFingerprint("a|b", "c", "d") {
		t.Fatal("fingerprints collide when text moves across the title/body boundary")
	}
	if prFingerprint("", "ab", "c") == prFingerprint("a", "b", "c") {
		t.Fatal("fingerprints collide for different splits of the same text")
	}
}

func TestNeedsPREdit(t *testing.T) {
	fp := prFingerprint("T", "B", "main")
	cases := []struct {
		name       string
		lastSynced string
		title      string
		body       string
		base       string
		want       bool
	}{
		{"never recorded (older PR) edits once", "", "T", "B", "main", true},
		{"unchanged skips the edit", fp, "T", "B", "main", false},
		{"title changed", fp, "T2", "B", "main", true},
		{"body changed", fp, "T", "B2", "main", true},
		{"base changed (retargeted)", fp, "T", "B", "feat-a-s1", true},
	}
	for _, c := range cases {
		if got := needsPREdit(c.lastSynced, c.title, c.body, c.base); got != c.want {
			t.Errorf("%s: needsPREdit = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestFormatSyncSummary(t *testing.T) {
	created := syncResult{Action: syncCreated, Number: 123, URL: "https://github.com/o/r/pull/123", Title: "Add header", StackSet: "feat-a"}
	updated := syncResult{Action: syncUpdated, Number: 124, URL: "https://github.com/o/r/pull/124", Title: "Add sidebar", StackSet: "feat-a"}
	same := syncResult{Action: syncUnchanged, Number: 125, StackSet: "feat-a"}

	cases := []struct {
		name    string
		results []syncResult
		want    []string
	}{
		{"nothing synced", nil, nil},
		{"mix keeps stack order and counts unchanged", []syncResult{created, same, updated, same},
			[]string{
				"Created PR #123  [feat-a]  Add header  https://github.com/o/r/pull/123",
				"Updated PR #124  [feat-a]  Add sidebar  https://github.com/o/r/pull/124",
				"2 PRs unchanged.",
			}},
		{"one unchanged is singular", []syncResult{same}, []string{"1 PR unchanged."}},
		{"all unchanged lists no PRs", []syncResult{same, same, same}, []string{"3 PRs unchanged."}},
		{"only changes prints no unchanged line", []syncResult{created}, []string{"Created PR #123  [feat-a]  Add header  https://github.com/o/r/pull/123"}},
	}
	for _, c := range cases {
		if got := formatSyncSummary(c.results); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got  %q\n want %q", c.name, got, c.want)
		}
	}
}
