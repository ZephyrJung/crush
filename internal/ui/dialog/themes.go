package dialog

import (
	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/list"
	"github.com/charmbracelet/crush/internal/ui/styles"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/sahilm/fuzzy"
)

const (
	ThemesID              = "themes"
	themesDialogMaxWidth  = 50
	themesDialogMaxHeight = 10
)

type Theme struct {
	ID          string
	Title       string
	Description string
}

var AllThemes = []Theme{
	{ID: "crush", Title: "Crush", Description: "Original Crush dark theme"},
	{ID: "zephyr", Title: "Zephyr", Description: "Cool low-contrast blue-cyan dark theme"},
}

type Themes struct {
	com   *common.Common
	help  help.Model
	list  *list.FilterableList
	input textinput.Model

	keyMap struct {
		Select   key.Binding
		Next     key.Binding
		Previous key.Binding
		UpDown   key.Binding
		Close    key.Binding
	}
}

type ThemeItem struct {
	*list.Versioned
	theme     Theme
	isCurrent bool
	t         *styles.Styles
	m         fuzzy.Match
	cache     map[int]string
	focused   bool
}

var (
	_ Dialog   = (*Themes)(nil)
	_ ListItem = (*ThemeItem)(nil)
)

func (t *ThemeItem) Finished() bool {
	return true
}

func NewThemes(com *common.Common) *Themes {
	t := &Themes{com: com}

	h := help.New()
	h.Styles = com.Styles.DialogHelpStyles()
	t.help = h

	t.list = list.NewFilterableList()
	t.list.Focus()

	t.input = textinput.New()
	t.input.SetVirtualCursor(false)
	t.input.Placeholder = "Type to filter"
	t.input.SetStyles(com.Styles.TextInput)
	t.input.Focus()

	t.keyMap.Select = key.NewBinding(
		key.WithKeys("enter", "ctrl+y"),
		key.WithHelp("enter", "confirm"),
	)
	t.keyMap.Next = key.NewBinding(
		key.WithKeys("down", "ctrl+n"),
		key.WithHelp("↓", "next item"),
	)
	t.keyMap.Previous = key.NewBinding(
		key.WithKeys("up", "ctrl+p"),
		key.WithHelp("↑", "previous item"),
	)
	t.keyMap.UpDown = key.NewBinding(
		key.WithKeys("up", "down"),
		key.WithHelp("↑/↓", "choose"),
	)
	t.keyMap.Close = CloseKey

	t.setItems()
	return t
}

func (t *Themes) ID() string {
	return ThemesID
}

func (t *Themes) HandleMsg(msg tea.Msg) Action {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, t.keyMap.Close):
			return ActionClose{}
		case key.Matches(msg, t.keyMap.Previous):
			t.list.Focus()
			if t.list.IsSelectedFirst() {
				t.list.SelectLast()
				t.list.ScrollToBottom()
				break
			}
			t.list.SelectPrev()
			t.list.ScrollToSelected()
		case key.Matches(msg, t.keyMap.Next):
			t.list.Focus()
			if t.list.IsSelectedLast() {
				t.list.SelectFirst()
				t.list.ScrollToTop()
				break
			}
			t.list.SelectNext()
			t.list.ScrollToSelected()
		case key.Matches(msg, t.keyMap.Select):
			selectedItem := t.list.SelectedItem()
			if selectedItem == nil {
				break
			}
			themeItem, ok := selectedItem.(*ThemeItem)
			if !ok {
				break
			}
			return ActionSelectTheme{Theme: themeItem.theme.ID}
		default:
			var cmd tea.Cmd
			t.input, cmd = t.input.Update(msg)
			value := t.input.Value()
			t.list.SetFilter(value)
			t.list.ScrollToTop()
			t.list.SetSelected(0)
			return ActionCmd{cmd}
		}
	}
	return nil
}

func (t *Themes) Cursor() *tea.Cursor {
	return InputCursor(t.com.Styles, t.input.Cursor())
}

func (t *Themes) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	sty := t.com.Styles
	width := max(0, min(themesDialogMaxWidth, area.Dx()))
	height := max(0, min(themesDialogMaxHeight, area.Dy()))
	innerWidth := width - sty.Dialog.View.GetHorizontalFrameSize()
	heightOffset := sty.Dialog.Title.GetVerticalFrameSize() + titleContentHeight +
		sty.Dialog.InputPrompt.GetVerticalFrameSize() + inputContentHeight +
		sty.Dialog.HelpView.GetVerticalFrameSize() +
		sty.Dialog.View.GetVerticalFrameSize()

	t.input.SetWidth(innerWidth - sty.Dialog.InputPrompt.GetHorizontalFrameSize() - 1)
	t.list.SetSize(innerWidth, height-heightOffset)
	t.help.SetWidth(innerWidth)

	rc := NewRenderContext(sty, width)
	rc.Title = "Themes"
	rc.AddPart(sty.Dialog.InputPrompt.Render(t.input.View()))
	rc.AddPart(sty.Dialog.List.Height(t.list.Height()).Render(t.list.Render()))
	rc.Help = t.help.View(t)

	cur := t.Cursor()
	DrawCenterCursor(scr, area, rc.Render(), cur)
	return cur
}

func (t *Themes) ShortHelp() []key.Binding {
	return []key.Binding{
		t.keyMap.UpDown,
		t.keyMap.Select,
		t.keyMap.Close,
	}
}

func (t *Themes) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{t.keyMap.Select, t.keyMap.Next, t.keyMap.Previous, t.keyMap.Close},
	}
}

func (t *Themes) setItems() {
	cfg := t.com.Config()
	currentTheme := ""
	if cfg != nil && cfg.Options != nil && cfg.Options.TUI != nil {
		currentTheme = cfg.Options.TUI.Theme
	}

	items := make([]list.FilterableItem, 0, len(AllThemes))
	selectedIndex := 0
	for i, theme := range AllThemes {
		item := &ThemeItem{
			Versioned: list.NewVersioned(),
			theme:     theme,
			isCurrent: theme.ID == currentTheme,
			t:         t.com.Styles,
		}
		items = append(items, item)
		if theme.ID == currentTheme {
			selectedIndex = i
		}
	}

	t.list.SetItems(items...)
	t.list.SetSelected(selectedIndex)
	t.list.ScrollToSelected()
}

func (t *ThemeItem) Filter() string {
	return t.theme.Title + " " + t.theme.Description
}

func (t *ThemeItem) ID() string {
	return t.theme.ID
}

func (t *ThemeItem) SetFocused(focused bool) {
	if t.focused == focused {
		return
	}
	t.cache = nil
	t.focused = focused
	if t.Versioned != nil {
		t.Bump()
	}
}

func (t *ThemeItem) SetMatch(m fuzzy.Match) {
	if sameFuzzyMatch(t.m, m) {
		return
	}
	t.cache = nil
	t.m = m
	if t.Versioned != nil {
		t.Bump()
	}
}

func (t *ThemeItem) Render(width int) string {
	info := ""
	if t.isCurrent {
		info = "current"
	}
	st := ListItemStyles{
		ItemBlurred:     t.t.Dialog.NormalItem,
		ItemFocused:     t.t.Dialog.SelectedItem,
		InfoTextBlurred: t.t.Dialog.ListItem.InfoBlurred,
		InfoTextFocused: t.t.Dialog.ListItem.InfoFocused,
	}
	return renderItem(st, t.theme.Title, info, t.focused, width, t.cache, &t.m)
}
