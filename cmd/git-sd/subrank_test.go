package main

import "testing"

func TestRankLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"1", "2", true},
		{"2", "1", false},
		{"1", "15", true}, // 0.1 < 0.15
		{"15", "2", true}, // 0.15 < 0.2
		{"9", "91", true}, // 0.9 < 0.91
		{"91", "9", false},
		{"5", "5", false},
		{"0", "1", true},
	}
	for _, c := range cases {
		if got := rankLess(c.a, c.b); got != c.want {
			t.Errorf("rankLess(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestNextRank(t *testing.T) {
	cases := []struct {
		name     string
		existing []string
		want     string
	}{
		{"none yet", nil, "1"},
		{"simple increment", []string{"1"}, "2"},
		{"mid-tier", []string{"1", "5"}, "6"},
		{"tier boundary", []string{"9"}, "91"},
		{"deeper tier boundary", []string{"91", "99"}, "991"},
		{"order independent", []string{"5", "1", "3"}, "6"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := nextRank(c.existing); got != c.want {
				t.Errorf("nextRank(%v) = %q, want %q", c.existing, got, c.want)
			}
		})
	}
}

// TestNextRankFullTierProgression walks the full documented sequence for
// repeated fixups against the same target, from SPEC.md's fixup section.
func TestNextRankFullTierProgression(t *testing.T) {
	want := []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "91", "92"}
	var existing []string
	for i, w := range want {
		got := nextRank(existing)
		if got != w {
			t.Fatalf("step %d: nextRank(%v) = %q, want %q", i, existing, got, w)
		}
		existing = append(existing, got)
	}
}

func TestParseAddress(t *testing.T) {
	cases := []struct {
		in       string
		wantIdx  int
		wantRank string
		wantErr  bool
	}{
		{"5", 5, "", false},
		{"0", 0, "", false},
		{"0.1", 0, "1", false},
		{"3.45", 3, "45", false},
		{"x", 0, "", true},
		{"3.", 0, "", true},
		{"", 0, "", true},
	}
	for _, c := range cases {
		idx, rank, err := parseAddress(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseAddress(%q): expected error, got none", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseAddress(%q): unexpected error: %v", c.in, err)
			continue
		}
		if idx != c.wantIdx || rank != c.wantRank {
			t.Errorf("parseAddress(%q) = (%d, %q), want (%d, %q)", c.in, idx, rank, c.wantIdx, c.wantRank)
		}
	}
}
