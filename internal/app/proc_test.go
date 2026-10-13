//go:build unix

package app

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestRunCommandStopsGently(t *testing.T) {
	for name, tc := range map[string]struct {
		script string
		want   int
	}{
		"default handling": {"echo ready; sleep 5", 128 + 15},
		"own handler":      {`trap "exit 42" TERM; echo ready; sleep 5 & wait`, 42},
		"plain exit":       {"exit 3", 3},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cmd := exec.CommandContext(ctx, "sh", "-c", tc.script)
			out, _ := cmd.StdoutPipe()
			go func() {
				buf := make([]byte, 6)
				_, _ = out.Read(buf) // "ready": the trap is set
				cancel()
			}()
			code, err := runCommand(cmd)
			if err != nil || code != tc.want {
				t.Fatalf("code=%d err=%v, want %d", code, err, tc.want)
			}
		})
	}

	if _, err := runCommand(exec.Command("/does/not/exist")); err == nil {
		t.Error("missing command: want error")
	}
}

func TestRunCommandKillsAfterTheGracePeriod(t *testing.T) {
	grace := stopGrace
	stopGrace = 200 * time.Millisecond
	t.Cleanup(func() { stopGrace = grace })
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "sh", "-c", `trap "" TERM; echo ready; while :; do sleep 0.1; done`)
	out, _ := cmd.StdoutPipe()
	go func() {
		buf := make([]byte, 6)
		_, _ = out.Read(buf)
		cancel()
	}()
	start := time.Now()
	code, _ := runCommand(cmd)
	if code != 128+9 || time.Since(start) < stopGrace {
		t.Fatalf("code=%d after %s", code, time.Since(start))
	}
}
