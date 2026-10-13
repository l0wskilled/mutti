package compose

// Feature is a Compose capability mutti relies on or uses when present.
type Feature struct {
	Name     string
	Since    Version
	Required bool   // mutti refuses to run without it
	Use      string // what mutti or projects need it for
}

// Features lists every Compose capability mutti depends on, oldest first.
var Features = []Feature{
	{Name: "up --wait", Since: MustParseVersion("2.1.1"), Required: true, Use: "mutti up waits for healthchecks"},
	{Name: "include", Since: MustParseVersion("2.20.0"), Use: "projects that split Compose files with include"},
	{Name: "watch", Since: MustParseVersion("2.22.0"), Use: "file sync with docker compose watch"},
	{Name: "!reset / !override", Since: MustParseVersion("2.24.4"), Required: true, Use: "mutti's override replaces published ports"},
}

// MinVersion is the oldest Compose mutti runs with: the newest Since of all required features.
func MinVersion() Version {
	var v Version
	for _, f := range Features {
		if f.Required && v.Less(f.Since) {
			v = f.Since
		}
	}
	return v
}

// Has reports whether version v has feature f.
func (v Version) Has(f Feature) bool { return !v.Less(f.Since) }
