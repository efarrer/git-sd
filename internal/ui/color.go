package ui

import "os"

// ANSI escape sequences for the colors a marker can have.
const (
	ansiReset  = "\x1b[0m"
	ansiDim    = "\x1b[2m"
	ansiRed    = "\x1b[31m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
)

// Colorize wraps s in the escape sequences for color. ColorNone, and an empty
// string, are returned unchanged. DisplayWidth ignores the escapes, so a
// colored cell aligns like a plain one.
func Colorize(color Color, s string) string {
	if s == "" {
		return s
	}
	var code string
	switch color {
	case ColorDim:
		code = ansiDim
	case ColorRed:
		code = ansiRed
	case ColorGreen:
		code = ansiGreen
	case ColorYellow:
		code = ansiYellow
	default:
		return s
	}
	return code + s + ansiReset
}

// ColorEnabled decides whether output should be colored: never unless it goes
// to a terminal, and not if sd.status.color is "false", NO_COLOR is set to a
// non-empty value (https://no-color.org), or TERM is "dumb". getenv reads the
// environment (os.Getenv in production).
func ColorEnabled(configured string, getenv func(string) string, isTerminal bool) bool {
	switch {
	case !isTerminal:
		return false
	case configured == "false":
		return false
	case getenv("NO_COLOR") != "":
		return false
	case getenv("TERM") == "dumb":
		return false
	}
	return true
}

// StdoutIsTerminal reports whether standard output is a terminal (a character
// device), as opposed to a pipe or a file.
func StdoutIsTerminal() bool {
	info, err := os.Stdout.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
