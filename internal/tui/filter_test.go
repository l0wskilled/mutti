package tui

import (
	"strings"
	"testing"

	"github.com/l0wskilled/mutti/internal/app"
)

func TestFilterInstances(t *testing.T) {
	w := setup(t)
	w.m.instances = append(w.m.instances, app.Summary{Name: "shop-billing", Path: "/billing", Branch: "feature/invoices"}, app.Summary{Name: "blog", Path: "/blog", Branch: "main"})
	press(w.m, "tab", "/")
	if w.m.mode != modeFilter || w.m.focus != paneInstances {
		t.Fatalf("mode %v focus %v", w.m.mode, w.m.focus)
	}
	// Typing narrows the list; the selected instance stays while it matches.
	press(w.m, "s", "h", "o", "p")
	if len(w.m.shown()) != 2 || w.m.selected != w.Root || !strings.Contains(w.screen(), "2 of 3") {
		t.Fatalf("shop: %v %q\n%s", len(w.m.shown()), w.m.selected, w.screen())
	}
	// A branch matches too, and the first match is selected once the selected one is gone.
	press(w.m, " ", "i", "n", "v")
	if got := w.m.shown(); len(got) != 1 || w.m.selected != "/billing" {
		t.Fatalf("invoices: %+v %q", got, w.m.selected)
	}
	press(w.m, "x")
	if len(w.m.shown()) != 0 || !strings.Contains(w.screen(), "nothing matches") || w.m.selected != "/billing" {
		t.Fatalf("no match: %q\n%s", w.m.selected, w.screen())
	}
	press(w.m, "backspace")
	// The arrows move through the matches; enter keeps the filter, esc drops it.
	press(w.m, "down", "up")
	if w.m.selected != "/billing" {
		t.Errorf("one match: %q", w.m.selected)
	}
	press(w.m, "enter")
	if w.m.mode != modeMain || w.m.filter.Value() != "shop inv" || len(w.m.shown()) != 1 {
		t.Fatalf("kept: %v %q", w.m.mode, w.m.filter.Value())
	}
	press(w.m, "/", "esc")
	if w.m.mode != modeMain || w.m.filter.Value() != "" || len(w.m.shown()) != 3 || strings.Contains(w.screen(), "of 3") {
		t.Fatalf("dropped: %v %q", w.m.mode, w.m.filter.Value())
	}
	w.m.mode = modeFilter
	w.m.Update(struct{}{}) // the cursor blinks
}
