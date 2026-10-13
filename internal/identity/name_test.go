package identity

import (
	"strings"
	"testing"
)

func TestSanitize(t *testing.T) {
	tests := map[string]string{
		"main":               "main",
		"Feature/ABC-123":    "feature-abc-123",
		"--weird__name..":    "weird-name",
		"ümlaut-ß":           "mlaut",
		"   ":                "",
		"a  b":               "a-b",
		"123":                "123",
		"my.project.v2":      "my-project-v2",
		"UPPER_and_lower-99": "upper-and-lower-99",
	}
	for in, want := range tests {
		if got := Sanitize(in); got != want {
			t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValid(t *testing.T) {
	for _, ok := range []string{"a", "main", "a-b", "0x", strings.Repeat("a", 63)} {
		if !Valid(ok) {
			t.Errorf("Valid(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", "-a", "a-", "A", "a_b", "a.b", strings.Repeat("a", 64)} {
		if Valid(bad) {
			t.Errorf("Valid(%q) = true", bad)
		}
	}
}

func TestInstanceName(t *testing.T) {
	if got := InstanceName("shop", "feature-x"); got != "shop-feature-x" {
		t.Errorf("got %q", got)
	}
	if got := InstanceName("shop", "shop"); got != "shop" {
		t.Errorf("main worktree: got %q", got)
	}
}

func TestShortenKeepsLongNamesDistinct(t *testing.T) {
	prefix := strings.Repeat("a", 70)
	a, b := Shorten(prefix+"-one"), Shorten(prefix+"-two")
	if a == b {
		t.Fatalf("distinct names collided: %q", a)
	}
	for _, n := range []string{a, b} {
		if !Valid(n) {
			t.Errorf("Shorten produced invalid %q (len %d)", n, len(n))
		}
	}
	if Shorten("short") != "short" {
		t.Error("short names must stay unchanged")
	}
}

func TestShortenNeverEndsWithDashBeforeHash(t *testing.T) {
	name := strings.Repeat("a", 53) + "-" + strings.Repeat("b", 20)
	got := Shorten(name)
	if strings.Contains(got, "--") || !Valid(got) {
		t.Errorf("Shorten(%q) = %q", name, got)
	}
}

func FuzzSanitizeShortenValid(f *testing.F) {
	for _, s := range []string{"main", "Feature/X", "", "---", strings.Repeat("ab-", 40)} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		clean := Sanitize(s)
		if clean == "" {
			return
		}
		if got := Shorten(clean); !Valid(got) {
			t.Fatalf("Shorten(Sanitize(%q)) = %q is not a valid label", s, got)
		}
		if Sanitize(clean) != clean {
			t.Fatalf("Sanitize is not idempotent for %q", s)
		}
	})
}
