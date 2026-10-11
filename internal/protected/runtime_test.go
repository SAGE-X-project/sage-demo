// SPDX-License-Identifier: LGPL-3.0-or-later
package protected

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sage-x-project/sage-demo/internal/relay"
	s "github.com/sage-x-project/sage-demo/internal/scenario"
	"github.com/sage-x-project/sage-demo/internal/transfer"
	r "github.com/sage-x-project/sage/pkg/agent/registry010"
)

// testClock follows real elapsed time plus an offset, so tests can pass the
// 360-second replay quarantine without waiting. The demo binary uses the
// system clock.
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

type world struct {
	opts   Options
	host   *ToolHostServer
	clock  *testClock
	ledger *transfer.Ledger
	agent  *AgentHost
	tool   string
}

// newWorld sets up a demo directory, serves both signers, opens both hosts
// and moves the clock past the replay quarantine. Each resource registers
// its cleanup as soon as it exists, so a failed step leaks nothing.
func newWorld(t *testing.T) *world {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "sd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	dir := filepath.Join(root, "d")
	if err := Setup(dir); err != nil {
		t.Fatal(err)
	}
	clock := &testClock{started: time.Now()}
	uid := uint32(os.Getuid())
	o := Options{Dir: dir, Create: true, Clock: clock, SignerUID: uid, Timeout: 3 * time.Second}
	for _, role := range []Role{Agent, ToolHost} {
		ctx, cancel := context.WithCancel(context.Background())
		ready, done := make(chan struct{}), make(chan error, 1)
		go func() { done <- ServeSigner(ctx, o, role, uid, func() { close(ready) }) }()
		t.Cleanup(func() {
			cancel()
			if err := <-done; err != nil {
				t.Errorf("signer %s: %v", role, err)
			}
		})
		select {
		case <-ready:
		case <-time.After(5 * time.Second):
			t.Fatalf("signer %s did not start", role)
		}
	}
	ledger, err := transfer.OpenLedger(Layout{Dir: dir}.Ledger("test"), s.OpeningBalance)
	if err != nil {
		t.Fatal(err)
	}
	tool, err := OpenToolHost(context.Background(), o, ledger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := tool.Close(context.Background()); err != nil {
			t.Errorf("tool host close: %v", err)
		}
	})
	agent, err := OpenAgent(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := agent.Close(); err != nil {
			t.Errorf("agent close: %v", err)
		}
	})
	clock.mono.Add(361000)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- tool.Serve(ctx, l) }()
	t.Cleanup(func() {
		cancel()
		if err := <-served; err != nil {
			t.Errorf("tool host serve: %v", err)
		}
	})
	return &world{opts: o, host: tool, clock: clock, ledger: ledger, agent: agent, tool: l.Addr().String()}
}

func (w *world) relay(t *testing.T, fault relay.Fault) *relay.Relay {
	t.Helper()
	rl, err := relay.Listen("127.0.0.1:0", w.tool, fault, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rl.Serve(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return rl
}

func (w *world) call(t *testing.T, addr string, arguments []byte) ([]byte, error) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return w.agent.Call(ctx, conn, arguments)
}

func (w *world) entries(t *testing.T) []transfer.Entry {
	t.Helper()
	got, err := w.ledger.Entries()
	if err != nil {
		t.Fatal(err)
	}
	return got
}

var approvedEntry = transfer.Entry{ID: "t-000001", Amount: 10, Currency: transfer.Currency, To: s.Recipient}

// The same faults as the baseline run, applied at the same point of the
// connection. The relay cannot read the protected exchange, so it changes
// bytes of the frame that carries the call or the result.
func TestProtectedScenarios(t *testing.T) {
	t.Run(string(s.Normal), func(t *testing.T) {
		w := newWorld(t)
		out, err := w.call(t, w.relay(t, relay.Pass{}).Addr(), s.Approved)
		if err != nil || string(out) != `{"balance":990,"transfer_id":"t-000001"}` {
			t.Fatalf("call = %s, %v", out, err)
		}
		if got := w.entries(t); !reflect.DeepEqual(got, []transfer.Entry{approvedEntry}) {
			t.Fatalf("ledger = %+v", got)
		}
	})

	t.Run(string(s.TamperArgs), func(t *testing.T) {
		w := newWorld(t)
		rl := w.relay(t, relay.FaultFunc(func(_ int, dir relay.Direction, i int, f []byte) []byte {
			if dir == relay.Request && i == RequestCall {
				return relay.Flip(f, len(f)/2)
			}
			return f
		}))
		if out, err := w.call(t, rl.Addr(), s.Approved); err == nil || out != nil {
			t.Fatalf("call = %s, %v; want refusal", out, err)
		}
		// The exchange reached the changed frame: Bob answered every frame
		// before it, so the refusal follows the change, not a broken setup.
		rec := rl.Records()[0]
		if len(rec.Requests) <= RequestCall || len(rec.Responses) < RequestCall {
			t.Fatalf("exchange stopped before the call frame: %d requests, %d responses", len(rec.Requests), len(rec.Responses))
		}
		if got := w.entries(t); len(got) != 0 {
			t.Fatalf("ledger = %+v", got)
		}
	})

	t.Run(string(s.Replay), func(t *testing.T) {
		w := newWorld(t)
		rl := w.relay(t, relay.Pass{})
		if _, err := w.call(t, rl.Addr(), s.Approved); err != nil {
			t.Fatal(err)
		}
		answers, err := relay.Replay(context.Background(), w.tool, rl.Records()[0].Requests, 3*time.Second)
		if err != nil || len(answers) != 0 {
			t.Fatalf("replay answered %d frames, %v; want none", len(answers), err)
		}
		if got := w.entries(t); !reflect.DeepEqual(got, []transfer.Entry{approvedEntry}) {
			t.Fatalf("ledger = %+v", got)
		}
	})

	t.Run(string(s.Unapproved), func(t *testing.T) {
		w := newWorld(t)
		rl := w.relay(t, relay.Pass{})
		if out, err := w.call(t, rl.Addr(), s.Substituted); err == nil || out != nil {
			t.Fatalf("call = %s, %v; want refusal", out, err)
		}
		for _, rec := range rl.Records() {
			if len(rec.Requests) != 0 {
				t.Fatalf("an unapproved call reached the relay: %d frames", len(rec.Requests))
			}
		}
		if got := w.entries(t); len(got) != 0 {
			t.Fatalf("ledger = %+v", got)
		}
	})

	t.Run(string(s.ForgeResult), func(t *testing.T) {
		w := newWorld(t)
		rl := w.relay(t, relay.FaultFunc(func(_ int, dir relay.Direction, i int, f []byte) []byte {
			if dir == relay.Response && i >= ResponseResult {
				return relay.Flip(f, len(f)/2)
			}
			return f
		}))
		if out, err := w.call(t, rl.Addr(), s.Approved); err == nil || out != nil {
			t.Fatalf("call = %s, %v; want refusal", out, err)
		}
		if got := w.entries(t); !reflect.DeepEqual(got, []transfer.Entry{approvedEntry}) {
			t.Fatalf("ledger = %+v", got)
		}
	})
}

// Control for the replay scenario: Bob answers a first handshake frame he
// has not seen, and refuses the same frame when it is resent. The resent
// connection therefore stops at the transport handshake replay check; the
// frame carrying the intent is never decrypted.
func TestReplayStopsAtTheHandshake(t *testing.T) {
	w := newWorld(t)
	sink, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sink.Close() })
	go func() {
		conn, err := sink.Accept()
		if err == nil {
			_, _ = relay.ReadFrame(conn)
			_ = conn.Close()
		}
	}()
	rl, err := relay.Listen("127.0.0.1:0", sink.Addr().String(), relay.Pass{}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rl.Serve(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	_, _ = w.call(t, rl.Addr(), s.Approved)
	first := rl.Records()[0].Requests
	if len(first) == 0 {
		t.Fatal("no handshake frame captured")
	}
	fresh, err := relay.Replay(context.Background(), w.tool, first[:1], time.Second)
	if err != nil || len(fresh) == 0 {
		t.Fatalf("fresh handshake answered %d frames, %v", len(fresh), err)
	}
	again, err := relay.Replay(context.Background(), w.tool, first[:1], time.Second)
	if err != nil || len(again) != 0 {
		t.Fatalf("resent handshake answered %d frames, %v", len(again), err)
	}
	if got := w.entries(t); len(got) != 0 {
		t.Fatalf("ledger = %+v", got)
	}
}
