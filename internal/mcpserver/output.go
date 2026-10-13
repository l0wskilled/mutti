package mcpserver

import (
	"fmt"
	"maps"
	"slices"
	"sync"
)

// outputLimit is how much of a command's output a result carries: the end, where errors and
// test summaries are, without filling the agent's context.
const outputLimit = 64 << 10

// capped keeps the last outputLimit bytes written to it. Commands write from several goroutines
// (stdout and stderr), so it locks.
type capped struct {
	mu        sync.Mutex
	b         []byte
	truncated bool
}

func (c *capped) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.b = append(c.b, p...)
	if len(c.b) > outputLimit {
		c.b = c.b[len(c.b)-outputLimit:]
		c.truncated = true
	}
	return len(p), nil
}

func (c *capped) result() Output { return Output{Output: c.text(), Truncated: c.cut()} }

func (c *capped) text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return string(c.b)
}

// cut reports whether the start of the output was dropped.
func (c *capped) cut() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.truncated
}

// withOutput adds what the commands printed to err: why compose failed is in there.
func withOutput(err error, out *capped) error {
	o := out.result()
	if o.Output == "" {
		return err
	}
	return fmt.Errorf("%w\n\noutput:\n%s", err, o.Output)
}

func sortedKeys(m map[string]string) []string { return slices.Sorted(maps.Keys(m)) }
