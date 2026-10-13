package compose

import (
	"strings"
	"testing"
)

func TestParseVersion(t *testing.T) {
	tests := map[string]Version{
		"2.24.4":            {2, 24, 4},
		"v2.29.1-desktop.1": {2, 29, 1},
		"5.5.1\n":           {5, 5, 1},
	}
	for in, want := range tests {
		got, err := ParseVersion(in)
		if err != nil || got != want {
			t.Errorf("ParseVersion(%q) = %v, %v", in, got, err)
		}
	}
	if _, err := ParseVersion("dev"); err == nil {
		t.Error("want error for a non-version")
	}
}

func TestVersionOrdering(t *testing.T) {
	ordered := []string{"1.29.2", "2.0.0", "2.24.3", "2.24.4", "2.25.0", "5.0.0"}
	for i := range len(ordered) - 1 {
		a, b := MustParseVersion(ordered[i]), MustParseVersion(ordered[i+1])
		if !a.Less(b) || b.Less(a) {
			t.Errorf("%v vs %v", a, b)
		}
	}
	if v := MustParseVersion("2.1.0"); v.Less(v) {
		t.Error("a version is not less than itself")
	}
	if MustParseVersion("2.24.4").String() != "2.24.4" {
		t.Error("String")
	}
}

func TestMustParseVersionPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil || !strings.Contains(r.(error).Error(), "unrecognized") {
			t.Fatalf("want panic, got %v", r)
		}
	}()
	MustParseVersion("x")
}

func TestMinVersionIsNewestRequiredFeature(t *testing.T) {
	if got := MinVersion(); got != (Version{2, 24, 4}) {
		t.Fatalf("MinVersion = %v", got)
	}
}

func TestHas(t *testing.T) {
	include := Features[1]
	if !MustParseVersion("2.20.0").Has(include) || MustParseVersion("2.19.9").Has(include) {
		t.Fatal("Has")
	}
}

func TestFeaturesAreSortedAndDocumented(t *testing.T) {
	for i, f := range Features {
		if f.Name == "" || f.Use == "" {
			t.Errorf("feature %d undocumented: %+v", i, f)
		}
		if i > 0 && f.Since.Less(Features[i-1].Since) {
			t.Errorf("%s is older than its predecessor", f.Name)
		}
	}
}
