package tui

import "charm.land/bubbles/v2/key"

// keyMap is every key of the main view; it doubles as the help.
type keyMap struct {
	Up, Down, Switch, Enter                           key.Binding
	Start, Stop, Restart, Down2, Destroy, Reset, Tidy key.Binding
	Open, Copy, Logs, Diagnose, Exec, Worktree        key.Binding
	Palette, Help, Quit                               key.Binding
}

func newKeyMap() keyMap {
	b := func(keys []string, k, desc string) key.Binding {
		return key.NewBinding(key.WithKeys(keys...), key.WithHelp(k, desc))
	}
	return keyMap{
		Up:       b([]string{"up", "k"}, "↑/k", "up"),
		Down:     b([]string{"down", "j"}, "↓/j", "down"),
		Switch:   b([]string{"tab", "left", "right", "h"}, "tab", "instances ⇄ services"),
		Enter:    b([]string{"enter"}, "enter", "services / shell"),
		Start:    b([]string{"u"}, "u", "start"),
		Stop:     b([]string{"s"}, "s", "stop"),
		Restart:  b([]string{"r"}, "r", "restart"),
		Down2:    b([]string{"d"}, "d", "down"),
		Destroy:  b([]string{"X"}, "X", "destroy"),
		Reset:    b([]string{"R"}, "R", "reset data"),
		Tidy:     b([]string{"T"}, "T", "tidy deleted worktrees"),
		Open:     b([]string{"o"}, "o", "open in browser"),
		Copy:     b([]string{"y"}, "y", "copy URL"),
		Logs:     b([]string{"l"}, "l", "logs"),
		Diagnose: b([]string{"i"}, "i", "diagnose"),
		Exec:     b([]string{"e"}, "e", "exec a command"),
		Worktree: b([]string{"n"}, "n", "new worktree"),
		Palette:  b([]string{":", "ctrl+k"}, ":", "all commands"),
		Help:     b([]string{"?"}, "?", "help"),
		Quit:     b([]string{"q", "ctrl+c"}, "q", "quit"),
	}
}

// ShortHelp is the footer: the keys that get someone going.
func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Switch, k.Start, k.Stop, k.Restart, k.Logs, k.Diagnose, k.Palette, k.Help, k.Quit}
}

// FullHelp is the help overlay, by topic.
func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.Switch, k.Enter, k.Palette, k.Help, k.Quit},
		{k.Start, k.Stop, k.Restart, k.Down2, k.Reset, k.Destroy, k.Tidy},
		{k.Open, k.Copy, k.Logs, k.Diagnose, k.Exec, k.Worktree},
	}
}
