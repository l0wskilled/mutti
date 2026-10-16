package tui

import (
	"context"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/l0wskilled/mutti/internal/app"
	"github.com/l0wskilled/mutti/internal/compose"
)

// mode is what the UI shows: the main view or one of the views over it.
type mode int

const (
	modeMain mode = iota
	modePalette
	modeFilter
	modePrompt
	modeConfirm
	modeLogs
	modeDiagnose
	modeHelp
	modeUwu
)

// pane is the half of the main view the keys act on.
type pane int

const (
	paneInstances pane = iota
	paneServices
)

// How often the UI looks again; tests shorten them.
var (
	refreshEvery  = 5 * time.Second        // statuses age ("Up 3 minutes") and health changes
	settleEvents  = 300 * time.Millisecond // a burst of events causes one refresh
	followLogs    = 2 * time.Second
	toastDuration = 4 * time.Second
)

const logLines = 300

// Messages.
type (
	instancesMsg struct {
		list []app.Summary
		err  error
	}
	statusMsg struct {
		path   string
		status *app.InstanceStatus
		diags  []app.Diagnosis
		err    error
	}
	eventMsg struct {
		ev   app.InstanceEvent
		open bool
	}
	tickMsg      struct{}
	refreshMsg   struct{}
	toastDoneMsg struct{ id int }
	doneMsg      struct {
		text, output, path string
		err                error
	}
	logsMsg struct {
		path, service, text string
	}
	logsTickMsg  struct{ gen int }
	diagnosisMsg struct {
		title string
		diags []app.Diagnosis
		err   error
	}
	routerMsg struct{ check app.Check }
	copyMsg   struct {
		url string
		err error
	}
)

type prompt struct {
	title string
	run   func(string) tea.Cmd
}

type confirmation struct {
	question string
	run      func() tea.Cmd
}

type model struct {
	ctx    context.Context
	opts   Options
	styles styles
	keys   keyMap
	width  int
	height int
	mode   mode
	focus  pane

	instances []app.Summary
	filter    textinput.Model // narrows the instances to those it matches
	selected  string          // path of the selected instance
	status    *app.InstanceStatus
	diags     map[string]app.Diagnosis
	statusErr error
	listErr   error
	router    *app.Check // nil until known
	svcCursor int
	marked    map[string]bool // services the actions act on together
	// Where the last render put the instances and services, for the mouse.
	instanceAt map[int]string                // line of the pane -> instance path
	serviceAt  map[int]int                   // line of the pane -> service index
	split      int                           // the column the services pane starts at
	details    map[string]app.ServiceDetails // by service
	usage      map[string]*usage             // by service, while it runs

	events  <-chan app.InstanceEvent
	pending bool // a refresh after events is on its way

	busy    string
	spinner spinner.Model
	toast   string
	toastID int
	toastOK bool

	palette list.Model
	input   textinput.Model
	prompt  prompt
	confirm confirmation
	help    help.Model

	viewer    viewport.Model // logs and diagnoses
	viewTitle string
	logsOf    string // services of the logs view, space-separated, "" for all
	follow    bool
	logsGen   int
}

func newModel(ctx context.Context, opts Options) *model {
	m := &model{ctx: ctx, opts: opts, styles: newStyles(true), keys: newKeyMap(), diags: map[string]app.Diagnosis{}, marked: map[string]bool{}, follow: true}
	m.forgetDetails()
	m.spinner = spinner.New(spinner.WithSpinner(spinner.MiniDot))
	m.input = textinput.New()
	m.input.Prompt = "› "
	m.filter = textinput.New()
	m.filter.Prompt = "/ "
	m.filter.Placeholder = "name or branch"
	m.help = help.New()
	m.viewer = viewport.New()
	m.viewer.SoftWrap = true
	m.palette = list.New(m.paletteItems(), list.NewDefaultDelegate(), 60, 20)
	m.palette.SetShowStatusBar(false)
	m.palette.SetShowHelp(false)
	m.palette.Filter = filterPalette
	m.palette.FilterInput.Prompt = "What do you want to do? › "
	m.palette.FilterInput.Placeholder = "start, logs, worktree, doctor…"
	return m
}

func (m *model) Init() tea.Cmd {
	a, _ := m.app()
	m.events, _ = a.Events(m.ctx)
	return tea.Batch(m.loadInstances(), m.loadRouter(), m.waitEvent(), tick(), sampleTick(), tea.RequestBackgroundColor)
}

// app is mutti's app with its output captured, so nothing writes over the UI.
func (m *model) app() (*app.App, *strings.Builder) {
	var out strings.Builder
	a := *m.opts.App
	a.IO = compose.IO{In: strings.NewReader(""), Out: &out, Err: &out}
	a.Host.StdinTTY = false
	return &a, &out
}

func tick() tea.Cmd { return tea.Tick(refreshEvery, func(time.Time) tea.Msg { return tickMsg{} }) }

func (m *model) waitEvent() tea.Cmd {
	ch := m.events
	return func() tea.Msg {
		ev, open := <-ch
		return eventMsg{ev: ev, open: open}
	}
}

func (m *model) loadInstances() tea.Cmd {
	a, _ := m.app()
	return func() tea.Msg {
		list, err := a.List(m.ctx)
		return instancesMsg{list: list, err: err}
	}
}

func (m *model) loadStatus() tea.Cmd {
	path := m.selected
	if path == "" {
		return nil
	}
	a, _ := m.app()
	return func() tea.Msg {
		st, err := a.Status(m.ctx, path)
		msg := statusMsg{path: path, status: st, err: err}
		if err == nil {
			msg.diags, _ = a.Diagnose(m.ctx, path)
		}
		return msg
	}
}

func (m *model) loadRouter() tea.Cmd {
	a, _ := m.app()
	return func() tea.Msg { return routerMsg{check: a.RouterCheck(m.ctx)} }
}

func (m *model) refresh() tea.Cmd {
	return tea.Batch(m.loadInstances(), m.loadStatus(), m.loadRouter())
}

// current is the selected instance, or nil.
func (m *model) current() *app.Summary {
	for i := range m.instances {
		if m.instances[i].Path == m.selected {
			return &m.instances[i]
		}
	}
	return nil
}

// services lists the services as the right pane shows them.
func (m *model) services() []app.ServiceStatus {
	if m.status == nil {
		return nil
	}
	return m.status.Services
}

// service is the selected service while the services pane has the focus, else "".
func (m *model) service() string {
	svcs := m.services()
	if m.focus != paneServices || len(svcs) == 0 {
		return ""
	}
	return svcs[min(m.svcCursor, len(svcs)-1)].Service
}

// target names what an action acts on: the marked services, else the selected one, else the
// instance.
func (m *model) target() (string, []string) {
	if marks := m.marks(); len(marks) > 0 && m.focus == paneServices {
		return strings.Join(marks, ", "), marks
	}
	if s := m.service(); s != "" {
		return s, []string{s}
	}
	if c := m.current(); c != nil {
		return c.Name, nil
	}
	return "", nil
}

func (m *model) setToast(text string, ok bool) tea.Cmd {
	m.toastID++
	m.toast, m.toastOK = text, ok
	id := m.toastID
	return tea.Tick(toastDuration, func(time.Time) tea.Msg { return toastDoneMsg{id: id} })
}

// act runs an action of the app in the background, with a spinner meanwhile.
func (m *model) act(busy string, run func(a *app.App) (string, error)) tea.Cmd {
	if m.busy != "" {
		return m.setToast("wait a moment, still "+m.busy, false)
	}
	m.busy = busy
	path := m.selected
	a, out := m.app()
	return tea.Batch(m.spinner.Tick, func() tea.Msg {
		text, err := run(a)
		return doneMsg{text: text, output: out.String(), path: path, err: err}
	})
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.resize(msg.Width, msg.Height)
		return m, nil
	case tea.BackgroundColorMsg:
		m.styles = newStyles(msg.IsDark())
		return m, nil
	case spinner.TickMsg:
		if m.busy == "" {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case instancesMsg:
		return m, m.gotInstances(msg)
	case statusMsg:
		m.gotStatus(msg)
		return m, nil
	case eventMsg:
		if !msg.open {
			return m, nil
		}
		if m.pending {
			return m, m.waitEvent()
		}
		m.pending = true
		return m, tea.Batch(m.waitEvent(), tea.Tick(settleEvents, func(time.Time) tea.Msg { return refreshMsg{} }))
	case refreshMsg:
		m.pending = false
		return m, m.refresh()
	case tickMsg:
		return m, tea.Batch(m.refresh(), tick())
	case sampleMsg:
		return m, tea.Batch(m.loadDetails(), sampleTick())
	case detailsMsg:
		m.gotDetails(msg)
		return m, nil
	case routerMsg:
		m.router = &msg.check
		return m, nil
	case toastDoneMsg:
		if msg.id == m.toastID {
			m.toast = ""
		}
		return m, nil
	case doneMsg:
		return m, m.done(msg)
	case execDoneMsg:
		return m, tea.Batch(m.refresh(), m.setToast(execResult(msg), msg.err == nil))
	case copyMsg:
		if msg.err != nil {
			return m, m.setToast(msg.err.Error(), false)
		}
		return m, tea.Batch(tea.SetClipboard(msg.url), m.setToast("copied "+msg.url, true))
	case logsMsg:
		if m.mode == modeLogs && msg.service == m.logsOf {
			atBottom := m.viewer.AtBottom()
			m.viewer.SetContent(m.colorLogs(msg.text))
			if atBottom || m.follow {
				m.viewer.GotoBottom()
			}
		}
		return m, nil
	case logsTickMsg:
		if m.mode != modeLogs || msg.gen != m.logsGen || !m.follow {
			return m, nil
		}
		return m, tea.Batch(m.loadLogs(), m.logsTick())
	case diagnosisMsg:
		m.openViewer(modeDiagnose, msg.title, m.renderDiagnoses(msg.diags, msg.err))
		return m, nil
	case tea.MouseClickMsg:
		return m, m.click(msg.Mouse())
	case tea.MouseWheelMsg:
		if m.mode == modeMain {
			return m, m.wheel(msg.Mouse())
		}
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "ctrl+l":
			return m, tea.ClearScreen // the terminal's redraw key
		}
		return m, m.key(msg)
	}
	return m, m.forward(msg)
}

// forward hands other messages to the component that has the focus.
func (m *model) forward(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	switch m.mode {
	case modePalette:
		m.palette, cmd = m.palette.Update(msg)
	case modePrompt:
		m.input, cmd = m.input.Update(msg)
	case modeFilter:
		m.filter, cmd = m.filter.Update(msg)
	case modeLogs, modeDiagnose:
		m.viewer, cmd = m.viewer.Update(msg)
	}
	return cmd
}

func (m *model) resize(w, h int) {
	m.width, m.height = w, h
	m.help.SetWidth(w)
	m.palette.SetSize(min(w-8, 90), max(h-10, 5))
	m.viewer.SetWidth(w - 4)
	m.viewer.SetHeight(max(h-6, 3))
	m.input.SetWidth(min(w-12, 70))
}

func (m *model) gotInstances(msg instancesMsg) tea.Cmd {
	m.listErr = msg.err
	if msg.err != nil {
		return nil
	}
	m.instances = msg.list
	if m.current() != nil {
		return nil
	}
	// The instance of the directory mutti runs in first, else the first one.
	m.selected = ""
	for _, s := range m.instances {
		if m.opts.Dir == s.Path || strings.HasPrefix(m.opts.Dir, s.Path+"/") {
			m.selected = s.Path
		}
	}
	if m.selected == "" && len(m.instances) > 0 {
		m.selected = m.instances[0].Path
	}
	m.status, m.svcCursor, m.marked = nil, 0, map[string]bool{}
	m.forgetDetails()
	return tea.Batch(m.loadStatus(), m.loadDetails())
}

func (m *model) gotStatus(msg statusMsg) {
	if msg.path != m.selected {
		return
	}
	m.status, m.statusErr = msg.status, msg.err
	m.diags = map[string]app.Diagnosis{}
	for _, d := range msg.diags {
		m.diags[d.Service] = d
	}
	if n := len(m.services()); m.svcCursor >= n {
		m.svcCursor = max(n-1, 0)
	}
}

func (m *model) done(msg doneMsg) tea.Cmd {
	m.busy = ""
	if msg.err == nil {
		return tea.Batch(m.refresh(), m.setToast(msg.text, true))
	}
	// What went wrong, with what the services say about it.
	path := msg.path
	a, _ := m.app()
	failure := msg.err.Error()
	if out := strings.TrimSpace(msg.output); out != "" {
		failure += "\n\n" + lastLines(out, 15)
	}
	return tea.Batch(m.refresh(), m.setToast(firstLine(msg.err.Error()), false), func() tea.Msg {
		diags, _ := a.Diagnose(m.ctx, path)
		return diagnosisMsg{title: "What went wrong", diags: append([]app.Diagnosis{{Service: "mutti", Problem: failure}}, diags...)}
	})
}

func execResult(msg execDoneMsg) string {
	if msg.err != nil {
		return msg.what + ": " + msg.err.Error()
	}
	return msg.what + ": done"
}

// key handles a key press in the current mode.
func (m *model) key(msg tea.KeyPressMsg) tea.Cmd {
	switch m.mode {
	case modePalette:
		return m.paletteKey(msg)
	case modePrompt:
		return m.promptKey(msg)
	case modeFilter:
		return m.filterKey(msg)
	case modeConfirm:
		m.mode = modeMain
		if msg.String() == "y" || msg.String() == "Y" {
			return m.confirm.run()
		}
		return m.setToast("cancelled", true)
	case modeLogs, modeDiagnose:
		return m.viewerKey(msg)
	case modeHelp, modeUwu:
		m.mode = modeMain
		return nil
	}
	return m.mainKey(msg)
}

func (m *model) mainKey(msg tea.KeyPressMsg) tea.Cmd {
	k := m.keys
	switch {
	case key.Matches(msg, k.Quit):
		return tea.Quit
	case key.Matches(msg, k.Up):
		return m.move(-1)
	case key.Matches(msg, k.Down):
		return m.move(1)
	case key.Matches(msg, k.Switch):
		m.switchPane()
	case key.Matches(msg, k.Mark):
		m.toggleMark()
	case msg.String() == "esc":
		m.marked = map[string]bool{}
	case key.Matches(msg, k.Filter):
		m.mode, m.focus = modeFilter, paneInstances
		return m.filter.Focus()
	case key.Matches(msg, k.Enter):
		return m.enter()
	case key.Matches(msg, k.Palette):
		m.mode = modePalette
		m.palette.SetFilterText("")              // everything, until something is typed
		m.palette.SetFilterState(list.Filtering) // type right away
		return nil
	case key.Matches(msg, k.Help):
		m.mode = modeHelp
	default:
		return m.actionKey(msg)
	}
	return nil
}

// move goes up or down in the focused pane; another instance gets its services loaded.
func (m *model) move(by int) tea.Cmd {
	if m.focus == paneServices {
		if n := len(m.services()); n > 0 {
			m.svcCursor = (m.svcCursor + by + n) % n
		}
		return nil
	}
	shown := m.shown()
	n := len(shown)
	if n == 0 {
		return nil
	}
	i := slices.IndexFunc(shown, func(s app.Summary) bool { return s.Path == m.selected })
	return m.selectInstance(shown[(i+by+n)%n].Path)
}

// marks are the marked services in the order of the services pane.
func (m *model) marks() []string {
	var out []string
	for _, svc := range m.services() {
		if m.marked[svc.Service] {
			out = append(out, svc.Service)
		}
	}
	return out
}

// toggleMark marks the service under the cursor, or unmarks it, and moves on.
func (m *model) toggleMark() {
	svc := m.service()
	if svc == "" {
		return
	}
	if m.marked[svc] {
		delete(m.marked, svc)
	} else {
		m.marked[svc] = true
	}
	m.move(1)
}

// selectInstance shows the instance at path, its status and details loaded anew.
func (m *model) selectInstance(path string) tea.Cmd {
	m.selected = path
	m.status, m.statusErr, m.svcCursor, m.diags, m.marked = nil, nil, 0, map[string]app.Diagnosis{}, map[string]bool{}
	m.forgetDetails()
	return tea.Batch(m.loadStatus(), m.loadDetails())
}

// shown are the instances the filter matches: those with every word of it in their name or
// branch.
func (m *model) shown() []app.Summary {
	words := strings.Fields(strings.ToLower(m.filter.Value()))
	var out []app.Summary
	for _, s := range m.instances {
		text := strings.ToLower(s.Name + " " + s.Branch)
		if !slices.ContainsFunc(words, func(w string) bool { return !strings.Contains(text, w) }) {
			out = append(out, s)
		}
	}
	return out
}

// filterKey edits the filter: enter keeps it, esc drops it, the arrows move through what it
// matches. The first match is selected when the selected instance no longer is one.
func (m *model) filterKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.filter.SetValue("")
		m.filter.Blur()
		m.mode = modeMain
		return nil
	case "enter":
		m.filter.Blur()
		m.mode = modeMain
		return nil
	case "up":
		return m.move(-1)
	case "down":
		return m.move(1)
	}
	var cmd tea.Cmd
	m.filter, cmd = m.filter.Update(msg)
	shown := m.shown()
	if len(shown) == 0 || slices.ContainsFunc(shown, func(s app.Summary) bool { return s.Path == m.selected }) {
		return cmd
	}
	return tea.Batch(cmd, m.selectInstance(shown[0].Path))
}

func (m *model) switchPane() {
	if m.focus == paneInstances && len(m.services()) > 0 {
		m.focus = paneServices
		return
	}
	m.focus = paneInstances
}

// enter opens the services of an instance, and a shell in a service.
func (m *model) enter() tea.Cmd {
	if m.focus == paneInstances {
		m.switchPane()
		return m.loadStatus()
	}
	if svc := m.service(); svc != "" {
		return m.runInTerminal(m.selected, "shell", svc)
	}
	return nil
}
