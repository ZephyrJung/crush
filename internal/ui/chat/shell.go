package chat

import (
	"fmt"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/ui/list"
	"github.com/charmbracelet/crush/internal/ui/styles"
)

// ShellOutputItem records a shell command that was run interactively.
type ShellOutputItem struct {
	*list.Versioned
	*cachedMessageItem

	sty     *styles.Styles
	command string
	err     error
}

// NewShellOutputItem creates a new ShellOutputItem.
func NewShellOutputItem(sty *styles.Styles, command string, err error) *ShellOutputItem {
	return &ShellOutputItem{
		Versioned:         list.NewVersioned(),
		cachedMessageItem: &cachedMessageItem{},
		sty:               sty,
		command:           command,
		err:               err,
	}
}

// ID implements MessageItem.
func (s *ShellOutputItem) ID() string {
	return s.command
}

// RawRender implements MessageItem.
func (s *ShellOutputItem) RawRender(width int) string {
	return s.Render(width)
}

// Finished implements list.Item.
func (s *ShellOutputItem) Finished() bool {
	return true
}

// Render implements list.Item.
func (s *ShellOutputItem) Render(width int) string {
	cappedWidth := cappedMessageWidth(width)
	content, _, ok := s.getCachedRender(cappedWidth)
	if ok {
		return content
	}

	prefix := s.sty.Messages.UserFocused.Render()
	status := s.sty.Tool.IconSuccess.Render("✔")
	if s.err != nil {
		status = s.sty.Tool.IconError.Render("✘")
	}
	result := prefix + " $ " + s.command + "  " + status

	s.setCachedRender(result, cappedWidth, lipgloss.Height(result))
	return result
}

// Sparkline renders a one-line compact version for the ending summary.
func (s *ShellOutputItem) Sparkline() string {
	status := "✔"
	if s.err != nil {
		status = fmt.Sprintf("✘ (%v)", s.err)
	}
	return fmt.Sprintf("$ %s  %s", s.command, status)
}