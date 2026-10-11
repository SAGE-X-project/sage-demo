// SPDX-License-Identifier: LGPL-3.0-or-later
package demo

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	r "github.com/sage-x-project/sage/pkg/agent/registry010"

	"github.com/sage-x-project/sage-demo/internal/protected"
)

// childEnv makes the test binary act as the demo binary, so that the Exec
// launcher can start real role processes from tests.
const childEnv = "SAGE_DEMO_TEST_CHILD=1"

func TestMain(m *testing.M) {
	if os.Getenv("SAGE_DEMO_TEST_CHILD") == "1" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		code := Main(ctx, os.Args[1:], os.Stdout, os.Stderr)
		stop()
		os.Exit(code)
	}
	os.Exit(m.Run())
}

// testClock follows real elapsed time plus an offset, so in-process runs can
// pass the 360-second replay quarantine without waiting. The demo binary uses
// the system clock.
type testClock struct {
	mono    atomic.Int64
	started time.Time
}

func (c *testClock) now() int64 { return c.mono.Load() + time.Since(c.started).Milliseconds() }

func (c *testClock) Now() (r.Stamp, error) {
	m := c.now()
	return r.Stamp{MonoMS: m, Unix: 100 + m/1000}, nil
}

func (c *testClock) Sample(context.Context) (int64, int64, error) {
	m := c.now()
	return 100000 + m, m, nil
}

// local runs every role in this process, sharing one test clock; the agent's
// wait advances the clock instead of sleeping.
type local struct{ clock *testClock }

type goroutine struct {
	cancel context.CancelFunc
	done   chan error
	once   sync.Once
	err    error
}

func (g *goroutine) Stop() error {
	g.once.Do(func() { g.cancel(); g.err = <-g.done })
	return g.err
}

func serve(ctx context.Context, run func(context.Context, func(string)) error) (*goroutine, string, error) {
	ctx, cancel := context.WithCancel(ctx)
	g := &goroutine{cancel: cancel, done: make(chan error, 1)}
	ready := make(chan string, 1)
	go func() { g.done <- run(ctx, func(addr string) { ready <- addr }) }()
	select {
	case addr := <-ready:
		return g, addr, nil
	case err := <-g.done:
		cancel()
		return nil, "", errors.Join(errors.New("role exited before it was ready"), err)
	case <-time.After(10 * time.Second):
		cancel()
		return nil, "", errors.New("role did not become ready")
	}
}

func (l local) Signer(ctx context.Context, dir string, role protected.Role) (Process, error) {
	g, _, err := serve(ctx, func(ctx context.Context, ready func(string)) error {
		return ServeSigner(ctx, dir, role, func() { ready("") })
	})
	return g, err
}

func (l local) ToolHost(ctx context.Context, c ToolHostConfig) (Process, string, error) {
	g, addr, err := serve(ctx, func(ctx context.Context, ready func(string)) error {
		return ServeToolHost(ctx, c, l.clock, ready)
	})
	if err != nil {
		return nil, "", err
	}
	return g, addr, nil
}

func (l local) Agent(ctx context.Context, c AgentConfig) (Outcome, error) {
	return RunAgent(ctx, c, l.clock, func(_ context.Context, d time.Duration) error {
		l.clock.mono.Add(d.Milliseconds())
		return nil
	})
}

// shortDir returns a fresh directory path short enough for signer sockets.
func shortDir(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "sd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return filepath.Join(root, "d")
}
