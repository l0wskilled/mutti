package tui

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/l0wskilled/mutti/internal/app"
	"github.com/l0wskilled/mutti/internal/compose"
)

// The logs view starts with logTail lines and keeps at most maxLogLines; older ones drop off.
const (
	logTail     = 1000
	maxLogLines = 10000
)

// logChunk is what the logs stream sends: lines, or at its end what ended it.
type logChunk struct {
	lines []string
	err   error
}

// logLinesMsg brings the lines that came in since the last one. ended marks the end of the
// stream, with err when Compose failed.
type logLinesMsg struct {
	gen   int
	lines []string
	ended bool
	err   error
}

// lineWriter collects what Compose prints into whole lines for the logs view. Compose writes
// stdout and stderr from goroutines of its own, so everything is under a lock.
type lineWriter struct {
	ctx     context.Context
	ch      chan<- logChunk
	mu      sync.Mutex
	partial []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.partial = append(w.partial, p...)
	i := bytes.LastIndexByte(w.partial, '\n')
	if i < 0 {
		return len(p), nil
	}
	lines := strings.Split(string(w.partial[:i]), "\n")
	w.partial = append([]byte(nil), w.partial[i+1:]...)
	w.send(logChunk{lines: lines})
	return len(p), nil
}

// end sends a line without its end and what ended the stream, then closes it.
func (w *lineWriter) end(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.partial) > 0 {
		w.send(logChunk{lines: []string{string(w.partial)}})
	}
	w.send(logChunk{err: err})
	close(w.ch)
}

// send waits for the view to take the chunk, unless the view went away.
func (w *lineWriter) send(c logChunk) {
	select {
	case w.ch <- c:
	case <-w.ctx.Done():
	}
}

// startLogs follows the logs of services, all of them without any, until stopLogs or the end
// of ctx.
func (m *model) startLogs(ctx context.Context, services []string) tea.Cmd {
	m.stopLogs()
	m.logsGen++
	m.logLines, m.logsEnded, m.logsErr, m.follow = nil, false, nil, true
	m.logFilter.SetValue("")
	m.renderLogs()
	ctx, cancel := context.WithCancel(ctx)
	ch := make(chan logChunk, 64)
	m.logsCancel, m.logsCh = cancel, ch
	a, _ := m.app()
	w := &lineWriter{ctx: ctx, ch: ch}
	a.IO = compose.IO{In: strings.NewReader(""), Out: w, Err: w}
	args := append([]string{"logs", "--no-color", "--timestamps", "--follow", "--tail", strconv.Itoa(logTail)}, services...)
	path := m.selected
	go func() {
		code, err := a.ComposePassthrough(ctx, path, app.Selection{}, args)
		if err == nil && code != 0 {
			err = fmt.Errorf("docker compose logs ended with exit code %d", code)
		}
		w.end(err)
	}()
	return m.nextLogs()
}

func (m *model) stopLogs() {
	if m.logsCancel != nil {
		m.logsCancel()
		m.logsCancel = nil
	}
}

// nextLogs waits for lines and takes every chunk already waiting along, so a chatty service
// costs one render, not one per line.
func (m *model) nextLogs() tea.Cmd {
	ch, gen := m.logsCh, m.logsGen
	return func() tea.Msg {
		msg := logLinesMsg{gen: gen}
		take := func(c logChunk, ok bool) bool {
			msg.lines = append(msg.lines, c.lines...)
			msg.err = cmp.Or(c.err, msg.err)
			msg.ended = !ok
			return ok
		}
		if c, ok := <-ch; !take(c, ok) {
			return msg
		}
		for {
			select {
			case c, ok := <-ch:
				if !take(c, ok) {
					return msg
				}
			default:
				return msg
			}
		}
	}
}

func (m *model) gotLogLines(msg logLinesMsg) tea.Cmd {
	if msg.gen != m.logsGen || m.mode != modeLogs {
		return nil // a stream the view no longer shows
	}
	m.logLines = append(m.logLines, msg.lines...)
	m.logLines = m.logLines[max(len(m.logLines)-maxLogLines, 0):]
	if msg.err != nil {
		m.logsErr = msg.err
	}
	m.logsEnded = msg.ended
	m.renderLogs()
	if msg.ended {
		return nil
	}
	return m.nextLogs()
}

// shownLogs are the lines the filter lets through: those with its text, in any case.
func (m *model) shownLogs() []string {
	term := strings.ToLower(m.logFilter.Value())
	if term == "" {
		return m.logLines
	}
	var out []string
	for _, l := range m.logLines {
		if strings.Contains(strings.ToLower(l), term) {
			out = append(out, l)
		}
	}
	return out
}

// renderLogs puts the shown lines into the view, at the bottom while it follows.
func (m *model) renderLogs() {
	shown := m.shownLogs()
	term := m.logFilter.Value()
	colored := make([]string, len(shown))
	for i, l := range shown {
		colored[i] = m.colorLine(l, term)
	}
	text := strings.Join(colored, "\n")
	switch {
	case len(m.logLines) == 0 && !m.logsEnded:
		text = m.styles.muted.Render("waiting for logs…")
	case len(shown) == 0 && term != "":
		text = m.styles.muted.Render("no line has " + strconv.Quote(term))
	}
	if m.logsErr != nil {
		text += "\n" + m.styles.errText.Render("✗ "+m.logsErr.Error())
	}
	m.viewer.SetContent(text)
	if m.follow {
		m.viewer.GotoBottom()
	}
}

// logsTitle is the title of the logs view: what it shows, its filter and whether it goes on.
func (m *model) logsTitle() string {
	title := m.viewTitle
	if term := m.logFilter.Value(); term != "" {
		title += fmt.Sprintf(" · %d of %d lines with %q", len(m.shownLogs()), len(m.logLines), term)
	}
	if m.logsEnded {
		title += " · ended"
	}
	return title
}

// logsKey: / filters, f follows, the arrows and the wheel scroll; scrolling up stops following.
func (m *model) logsKey(msg tea.KeyPressMsg) tea.Cmd {
	if m.logFiltering {
		switch msg.String() {
		case "esc":
			m.logFilter.SetValue("")
			fallthrough
		case "enter":
			m.logFiltering = false
			m.logFilter.Blur()
			m.renderLogs()
			return nil
		}
		var cmd tea.Cmd
		m.logFilter, cmd = m.logFilter.Update(msg)
		m.renderLogs()
		return cmd
	}
	switch msg.String() {
	case "/":
		m.logFiltering = true
		return m.logFilter.Focus()
	case "f":
		m.follow = !m.follow
		if m.follow {
			m.viewer.GotoBottom()
		}
		return nil
	}
	return m.scrollLogs(msg)
}

// scrollLogs hands a key or the wheel to the view; it follows while it is at the bottom.
func (m *model) scrollLogs(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	m.viewer, cmd = m.viewer.Update(msg)
	m.follow = m.viewer.AtBottom()
	return cmd
}

// logLine splits "web-1  | 2026-...Z message" into its parts.
var logLine = regexp.MustCompile(`^(\S+\s*\|\s)(\S+Z\s)?(.*)$`)

// colorLogs colors every line of text, as colorLine does.
func (m *model) colorLogs(text string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, l := range lines {
		lines[i] = m.colorLine(l, "")
	}
	return strings.Join(lines, "\n")
}

// colorLine sets the service and the time apart, shows errors and warnings in color and marks
// where the message has term.
func (m *model) colorLine(l, term string) string {
	s := m.styles
	msg := s.title.Faint(false).Bold(false)
	lower := strings.ToLower(l)
	switch {
	case strings.Contains(lower, "error"), strings.Contains(lower, "fatal"), strings.Contains(lower, "panic"), strings.Contains(lower, "exception"):
		msg = s.errText
	case strings.Contains(lower, "warn"):
		msg = msg.Foreground(s.amber)
	}
	p := logLine.FindStringSubmatch(l)
	if p == nil {
		return mark(l, term, msg)
	}
	return s.accentText.Render(p[1]) + s.faintText.Render(p[2]) + mark(p[3], term, msg)
}

// mark renders text in st with every match of term, in any case, reversed.
func mark(text, term string, st lipgloss.Style) string {
	if term == "" {
		return st.Render(text)
	}
	lower, needle := strings.ToLower(text), strings.ToLower(term)
	if len(lower) != len(text) { // a case change that alters the length: offsets would not match
		return st.Render(text)
	}
	var b strings.Builder
	for {
		i := strings.Index(lower, needle)
		if i < 0 {
			b.WriteString(st.Render(text))
			return b.String()
		}
		b.WriteString(st.Render(text[:i]) + st.Reverse(true).Render(text[i:i+len(needle)]))
		text, lower = text[i+len(needle):], lower[i+len(needle):]
	}
}
