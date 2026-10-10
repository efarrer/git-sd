package ui

import "unicode"

// DisplayWidth returns how many terminal columns s occupies: emoji and CJK
// characters take two, combining marks, variation selectors and ANSI color
// escape sequences take none, everything else one. (len(s) counts bytes and
// utf8.RuneCountInString counts characters; neither is what a terminal shows.)
func DisplayWidth(s string) int {
	w := 0
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if r == 0x1b && i+1 < len(rs) && rs[i+1] == '[' {
			// CSI sequence (colors etc.): skip through its final byte.
			i += 2
			for i < len(rs) && (rs[i] < 0x40 || rs[i] > 0x7e) {
				i++
			}
			continue
		}
		w += runeWidth(r)
	}
	return w
}

func runeWidth(r rune) int {
	switch {
	case r < 0x20 || (r >= 0x7f && r < 0xa0):
		return 0
	case r == 0x200d || (r >= 0xfe00 && r <= 0xfe0f):
		return 0 // zero-width joiner, variation selectors
	case unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf):
		return 0
	case isWide(r):
		return 2
	}
	return 1
}

// wideRanges are the code point ranges terminals draw two columns wide:
// emoji presentation characters and East Asian wide/fullwidth characters.
var wideRanges = [][2]rune{
	{0x1100, 0x115f}, {0x231a, 0x231b}, {0x23e9, 0x23ec}, {0x23f0, 0x23f0}, {0x23f3, 0x23f3},
	{0x25fd, 0x25fe}, {0x2614, 0x2615}, {0x2648, 0x2653}, {0x267f, 0x267f}, {0x2693, 0x2693},
	{0x26a1, 0x26a1}, {0x26aa, 0x26ab}, {0x26bd, 0x26be}, {0x26c4, 0x26c5}, {0x26ce, 0x26ce},
	{0x26d4, 0x26d4}, {0x26ea, 0x26ea}, {0x26f2, 0x26f3}, {0x26f5, 0x26f5}, {0x26fa, 0x26fa},
	{0x26fd, 0x26fd}, {0x2705, 0x2705}, {0x270a, 0x270b}, {0x2728, 0x2728}, {0x274c, 0x274c},
	{0x274e, 0x274e}, {0x2753, 0x2755}, {0x2757, 0x2757}, {0x2795, 0x2797}, {0x27b0, 0x27b0},
	{0x27bf, 0x27bf}, {0x2b1b, 0x2b1c}, {0x2b50, 0x2b50}, {0x2b55, 0x2b55},
	{0x2e80, 0x303e}, {0x3041, 0x33ff}, {0x3400, 0x4dbf}, {0x4e00, 0xa4cf}, {0xac00, 0xd7a3},
	{0xf900, 0xfaff}, {0xfe30, 0xfe6f}, {0xff00, 0xff60}, {0xffe0, 0xffe6},
	{0x1f1e6, 0x1f1ff}, {0x1f300, 0x1f64f}, {0x1f680, 0x1f6ff}, {0x1f7e0, 0x1f7eb},
	{0x1f900, 0x1f9ff}, {0x1fa70, 0x1faff},
}

func isWide(r rune) bool {
	for _, rg := range wideRanges {
		if r >= rg[0] && r <= rg[1] {
			return true
		}
	}
	return false
}
