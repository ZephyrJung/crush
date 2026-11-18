package model

import (
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/workspace"
)

// stubWorkspace is a minimal [workspace.Workspace] for tests that only need
// config and working-directory access.
type stubWorkspace struct {
	workspace.Workspace
}

func (w *stubWorkspace) Config() *config.Config { return &config.Config{} }
func (w *stubWorkspace) WorkingDir() string     { return "/" }

func newStubUI() *UI {
	com := common.DefaultCommon(&stubWorkspace{})
	return &UI{
		com:     com,
		status:  NewStatus(com, nil),
		chat:    NewChat(com, config.ScrollbarDefault),
		state:   uiChat,
		focus:   uiFocusEditor,
		width:   140,
		height:  45,
		session: &session.Session{ID: "s1"},
		keyMap:  DefaultKeyMap(),
		dialog:  dialog.NewOverlay(),
	}
}
