package dialog

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/crush/internal/workspace"
	"github.com/stretchr/testify/require"
)

func TestThemesSelectsTheme(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Options: &config.Options{
			TUI: &config.TUIOptions{},
		},
	}
	d := NewThemes(&common.Common{
		Styles: &styles.Styles{},
		Workspace: &themeTestWorkspace{
			cfg: cfg,
		},
	})

	action := d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})

	selectTheme, ok := action.(ActionSelectTheme)
	require.True(t, ok)
	require.Equal(t, "crush", selectTheme.Theme)
}

type themeTestWorkspace struct {
	workspace.Workspace
	cfg *config.Config
}

func (w *themeTestWorkspace) Config() *config.Config {
	return w.cfg
}
