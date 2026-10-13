package app

import (
	"fmt"
	"strings"

	"github.com/l0wskilled/mutti/internal/identity"
)

// DefaultDomain is the base domain of every hostname. RFC 6761 reserves it for loopback, so it
// resolves without any DNS setup and browsers treat it as a secure context.
const DefaultDomain = "localhost"

// DomainFromEnv returns the base domain from $MUTTI_DOMAIN, or DefaultDomain.
func DomainFromEnv(getenv func(string) string) (string, error) {
	d := getenv("MUTTI_DOMAIN")
	if d == "" {
		return DefaultDomain, nil
	}
	for _, label := range strings.Split(d, ".") {
		if !identity.Valid(label) {
			return "", &Error{
				Code:    CodeInvalidConfig,
				Message: fmt.Sprintf("MUTTI_DOMAIN=%q is not a domain", d),
				Hint:    "use lower case labels separated by dots, e.g. test",
			}
		}
	}
	return d, nil
}
