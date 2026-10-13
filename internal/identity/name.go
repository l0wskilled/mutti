// Package identity derives, stores and validates instance names.
package identity

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// MaxLen is the DNS label limit, which also bounds Compose project names and hostnames.
const MaxLen = 63

var validLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// Sanitize turns an arbitrary string into a DNS label: lower case, every run of other characters
// becomes one "-", no leading or trailing "-". The result may be empty and is not length-capped.
func Sanitize(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
			continue
		}
		dash = true
	}
	return b.String()
}

// Valid reports whether name is a usable DNS label.
func Valid(name string) bool {
	return len(name) <= MaxLen && validLabel.MatchString(name)
}

// InstanceName joins project and worktree names into the instance name, which is also the
// Compose project name. A worktree named like its project (usually the main worktree) is just the
// project. Names longer than MaxLen are cut and get a hash of the full name, so two long names
// sharing a prefix stay distinct.
func InstanceName(project, worktree string) string {
	name := project
	if worktree != project {
		name = project + "-" + worktree
	}
	return Shorten(name)
}

// Shorten caps name at MaxLen, appending a hash of the full name when it had to cut.
func Shorten(name string) string {
	if len(name) <= MaxLen {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	suffix := "-" + hex.EncodeToString(sum[:])[:8]
	head := strings.TrimRight(name[:MaxLen-len(suffix)], "-")
	return head + suffix
}
