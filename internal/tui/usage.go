package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/l0wskilled/mutti/internal/app"
)

// sampleEvery is how often the UI asks what the services run and use; tests shorten it.
var sampleEvery = 2 * time.Second

// historyLen is how many samples a sparkline shows at most.
const historyLen = 30

type (
	sampleMsg  struct{}
	detailsMsg struct {
		instance string
		list     []app.ServiceDetails
		err      error
	}
)

// usage is what a service used over the last samples.
type usage struct {
	last app.Usage
	cpu  []float64 // percent of one core
	mem  []float64 // bytes
}

// add takes a sample. CPU use needs two: it is the CPU time used between them.
func (u *usage) add(s app.Usage) {
	if !s.Sampled.After(u.last.Sampled) {
		return
	}
	if !u.last.Sampled.IsZero() && s.CPU >= u.last.CPU { // less CPU time: it restarted
		u.cpu = appendCapped(u.cpu, float64(s.CPU-u.last.CPU)/float64(s.Sampled.Sub(u.last.Sampled))*100)
	}
	u.mem = appendCapped(u.mem, float64(s.Memory))
	u.last = s
}

func appendCapped(values []float64, v float64) []float64 {
	values = append(values, v)
	return values[max(len(values)-historyLen, 0):]
}

func sampleTick() tea.Cmd {
	return tea.Tick(sampleEvery, func(time.Time) tea.Msg { return sampleMsg{} })
}

func (m *model) loadDetails() tea.Cmd {
	c := m.current()
	if c == nil || c.Orphaned {
		return nil
	}
	a, _ := m.app()
	name := c.Name
	return func() tea.Msg {
		list, err := a.Details(m.ctx, name)
		return detailsMsg{instance: name, list: list, err: err}
	}
}

func (m *model) gotDetails(msg detailsMsg) {
	if c := m.current(); c == nil || c.Name != msg.instance || msg.err != nil {
		return // the status says what is wrong with Docker
	}
	m.details = map[string]app.ServiceDetails{}
	for _, d := range msg.list {
		m.details[d.Service] = d
		if d.Usage == nil {
			delete(m.usage, d.Service) // it stopped: its history ends
			continue
		}
		u := m.usage[d.Service]
		if u == nil {
			u = &usage{}
			m.usage[d.Service] = u
		}
		u.add(*d.Usage)
	}
}

// forgetDetails drops what the UI knows about the services of the instance it showed.
func (m *model) forgetDetails() {
	m.details, m.usage = map[string]app.ServiceDetails{}, map[string]*usage{}
}

var sparks = []rune("▁▂▃▄▅▆▇█")

// sparkline draws the last width values between lo and hi, right-aligned.
func sparkline(values []float64, width int, lo, hi float64) string {
	values = values[max(len(values)-width, 0):]
	var b strings.Builder
	b.WriteString(strings.Repeat(" ", width-len(values)))
	for _, v := range values {
		i := 0
		if hi > lo {
			i = min(max(int((v-lo)/(hi-lo)*float64(len(sparks)-1)+0.5), 0), len(sparks)-1)
		}
		b.WriteRune(sparks[i])
	}
	return b.String()
}

// cpuLine is the CPU sparkline: from zero to the highest value, at least 5%, so an idle
// service stays flat.
func cpuLine(values []float64, width int) string {
	hi := 5.0
	for _, v := range values {
		hi = max(hi, v)
	}
	return sparkline(values, width, 0, hi)
}

// memLine is the memory sparkline around the values it shows. It spans at least a quarter of
// the highest, so a few kilobytes up and down stay a ripple.
func memLine(values []float64, width int) string {
	values = values[max(len(values)-width, 0):]
	lo, hi := values[0], values[0]
	for _, v := range values {
		lo, hi = min(lo, v), max(hi, v)
	}
	span, mid := max(hi-lo, hi/4), (hi+lo)/2
	return sparkline(values, width, mid-span/2, mid+span/2)
}

func last(values []float64) float64 { return values[len(values)-1] }

func humanBytes(b float64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1fG", b/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.0fM", b/(1<<20))
	}
	return fmt.Sprintf("%.0fK", b/(1<<10))
}

func humanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
}
