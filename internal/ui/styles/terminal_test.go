package styles

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestColorFGBGIsDark(t *testing.T) {
	tests := []struct {
		env  string
		want bool
	}{
		{"", true},
		{"15;0", true},   // black background
		{"0;7", false},   // white background
		{"15;15", false}, // bright white background
		{"garbage", true},
		{"7", true}, // no bg part
	}
	for _, tt := range tests {
		t.Run(tt.env, func(t *testing.T) {
			t.Setenv("COLORFGBG", tt.env)
			require.Equal(t, tt.want, colorFGBGIsDark())
		})
	}
}

func TestTerminalIsDarkOverride(t *testing.T) {
	ClearTerminalIsDark()
	defer ClearTerminalIsDark()

	// Default (no override, no COLORFGBG): dark.
	t.Setenv("COLORFGBG", "")
	require.True(t, TerminalIsDark())

	// Override wins once set.
	SetTerminalIsDark(false)
	require.False(t, TerminalIsDark())
	SetTerminalIsDark(true)
	require.True(t, TerminalIsDark())

	ClearTerminalIsDark()
	require.True(t, TerminalIsDark())
}

func TestDefaultThemeFollowsTerminalMode(t *testing.T) {
	ClearTerminalIsDark()
	defer ClearTerminalIsDark()

	SetTerminalIsDark(true)
	require.Equal(t, "charmtone-panther", DefaultThemeName())
	require.Equal(t, ThemeKeyDefault, ThemeKeyForProvider("ollama"))
	require.Equal(t, CharmtonePantera().Background, ThemeForKey(ThemeKeyDefault).Background)
	require.Equal(t, CharmtonePantera().Background, ThemeForProvider("ollama").Background)

	empty, err := LoadTheme("")
	require.NoError(t, err)
	require.Equal(t, CharmtonePantera().Background, empty.Background)

	SetTerminalIsDark(false)
	require.Equal(t, "charmtone-daylight", DefaultThemeName())
	require.Equal(t, ThemeKeyDefaultLight, ThemeKeyForProvider("ollama"))

	light := ThemeForKey(ThemeKeyDefaultLight)
	require.Equal(t, CharmtoneDaylight().Background, light.Background)
	require.Equal(t, "#f7f6fb", *hex(light.Background))
	require.Equal(t, "#3a3943", *hex(light.Messages.NoContent.GetForeground()))

	empty, err = LoadTheme("")
	require.NoError(t, err)
	require.Equal(t, light.Background, empty.Background)
}

func TestCharmtoneDaylightIsLight(t *testing.T) {
	// The daylight theme must paint light surfaces with dark ink: verify
	// a few core pairings keep WCAG AA contrast on the light background.
	const minAA = 4.5
	s := CharmtoneDaylight()
	bg := *hex(s.Background)
	require.Equal(t, "#f7f6fb", bg)
	for name, fg := range map[string]*string{
		"body text": hex(s.Messages.NoContent.GetForeground()),
		"editor":    hex(s.TextInput.Focused.Text.GetForeground()),
	} {
		require.NotNil(t, fg, name)
		if ratio := contrastRatio(*fg, bg); ratio < minAA {
			t.Errorf("%s contrast %.2f is below WCAG AA on the light background", name, ratio)
		}
	}
	// And it must differ from the dark theme.
	require.NotEqual(t, CharmtonePantera().Background, s.Background)
}

func TestDaylightRegisteredAsBuiltin(t *testing.T) {
	// Sanity: the daylight palette resolves through the theme pipeline.
	require.True(t, IsBuiltinTheme("charmtone-daylight"))
	s, err := LoadTheme("Charmtone-Daylight")
	require.NoError(t, err)
	require.NotNil(t, s.WorkingGradFromColor)
	require.Equal(t, "#6b50ff", rgbHex(s.WorkingGradFromColor))
}
