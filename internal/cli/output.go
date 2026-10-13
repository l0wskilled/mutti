package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/l0wskilled/mutti/internal/app"
)

// SchemaVersion of the --json output. Fields are only ever added; a breaking change bumps it.
const SchemaVersion = 1

// Error codes of the command line itself; the others are app's.
const (
	CodeUsage                = "usage"                 // wrong flags or arguments
	CodeConfirmationRequired = "confirmation-required" // destructive without --yes and no terminal
	CodeError                = "error"                 // a failure without a code of its own
)

// Exit codes of mutti's own failures, following Docker's convention.
const (
	ExitError = 125 // mutti itself failed
	ExitCheck = 1   // a check (doctor) found problems
)

type envelope struct {
	SchemaVersion int        `json:"schemaVersion"`
	Data          any        `json:"data,omitempty"`
	Error         *jsonError `json:"error,omitempty"`
}

type jsonError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

// printer renders results as text or as versioned JSON.
type printer struct {
	json     bool
	out, err io.Writer
}

// result prints data; text renders the human version.
func (p printer) result(data any, text func(w io.Writer)) {
	if p.json {
		enc := json.NewEncoder(p.out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(envelope{SchemaVersion: SchemaVersion, Data: data})
		return
	}
	text(p.out)
}

// stream prints one item of a stream: with --json an envelope on a line of its own, so a reader
// can take every line as it comes.
func (p printer) stream(data any, text func(w io.Writer)) {
	if p.json {
		_ = json.NewEncoder(p.out).Encode(envelope{SchemaVersion: SchemaVersion, Data: data})
		return
	}
	text(p.out)
}

// failure prints err and returns the exit code for it.
// The code and hint come from the first app.Error in err; the message is all of err, so a
// failure joined to another one (a teardown after a failed run) is not lost.
func (p printer) failure(err error) int {
	code, hint := CodeError, ""
	var appErr *app.Error
	switch {
	case errors.As(err, &appErr):
		code, hint = appErr.Code, appErr.Hint
	case errors.Is(err, context.Canceled):
		code = app.CodeCancelled
	}
	if p.json {
		enc := json.NewEncoder(p.err)
		enc.SetIndent("", "  ")
		_ = enc.Encode(envelope{SchemaVersion: SchemaVersion, Error: &jsonError{Code: code, Message: err.Error(), Hint: hint}})
		return ExitError
	}
	fmt.Fprintf(p.err, "mutti: %s\n", err.Error())
	if hint != "" {
		fmt.Fprintf(p.err, "hint: %s\n", hint)
	}
	return ExitError
}
