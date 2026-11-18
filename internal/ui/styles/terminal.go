package styles

import (
	"os"
	"strings"
	"sync/atomic"
)

// The terminal background decides which of the two default themes —
// dark Charmtone Panther for night, light Charmtone Daylight for day —
// applies when the user has not picked a theme explicitly.
//
// Selection is cheap and non-blocking: it reads an override the UI sets
// when the terminal reports its background color (Bubble Tea queries it
// asynchronously at startup and whenever it changes), falling back to
// the COLORFGBG environment variable and finally to dark. Deliberately
// no OSC query happens here: it would stall startup on terminals that
// don't answer it.

var (
	overrideSet atomic.Bool
	overrideVal atomic.Bool

	envFallbackOnce atomic.Bool
	envFallbackVal  atomic.Bool
)

// TerminalIsDark reports whether the terminal uses a dark background.
// The value reflects the last report from the running UI; before that
// it falls back to COLORFGBG and finally to true (dark), so
// non-interactive contexts keep the classic dark look.
func TerminalIsDark() bool {
	if overrideSet.Load() {
		return overrideVal.Load()
	}
	if envFallbackOnce.CompareAndSwap(false, true) {
		envFallbackVal.Store(colorFGBGIsDark())
	}
	return envFallbackVal.Load()
}

// SetTerminalIsDark overrides the terminal background used for default
// theme selection. The UI calls it when the terminal reports (or
// switches) its background at runtime; tests use it to pin a mode.
func SetTerminalIsDark(dark bool) {
	overrideSet.Store(true)
	overrideVal.Store(dark)
}

// ClearTerminalIsDark drops a previously set override, so selection
// falls back to the environment fallback again.
func ClearTerminalIsDark() {
	overrideSet.Store(false)
}

// colorFGBGIsDark reads the COLORFGBG environment variable ("fg;bg",
// both ANSI color indexes). Many terminal emulators set it; a light
// default background (index 7 or 15) reports light. Anything else —
// including the variable being unset — reports dark.
func colorFGBGIsDark() bool {
	v := os.Getenv("COLORFGBG")
	if v == "" {
		return true
	}
	parts := strings.SplitN(v, ";", 2)
	if len(parts) < 2 {
		return true
	}
	switch strings.TrimSpace(parts[1]) {
	case "7", "15":
		// White-ish default backgrounds: white and bright white.
		return false
	}
	return true
}
