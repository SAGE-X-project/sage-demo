// SPDX-License-Identifier: LGPL-3.0-or-later
package baseline

import (
	"bytes"
	"context"
	"errors"
	"net"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/sage-x-project/sage-demo/internal/relay"
	s "github.com/sage-x-project/sage-demo/internal/scenario"
	"github.com/sage-x-project/sage-demo/internal/transfer"
)

type run struct {
	ledger *transfer.Ledger
	host   string
	relay  *relay.Relay
}

func start(t *testing.T, fault relay.Fault) *run {
	t.Helper()
	ledger, err := transfer.OpenLedger(filepath.Join(t.TempDir(), "ledger.jsonl"), s.OpeningBalance)
	if err != nil {
		t.Fatal(err)
	}
	host, err := NewHost(ledger, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r, err := relay.Listen("127.0.0.1:0", l.Addr().String(), fault, time.Second)
	if err != nil {
		_ = l.Close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 2)
	go func() { served <- host.Serve(ctx, l) }()
	go func() { served <- r.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		for range 2 {
			if err := <-served; err != nil {
				t.Errorf("serve: %v", err)
			}
		}
	})
	return &run{ledger: ledger, host: l.Addr().String(), relay: r}
}

func (r *run) call(t *testing.T, arguments []byte) ([]byte, error) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", r.relay.Addr(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return Call(ctx, conn, arguments)
}

func (r *run) entries(t *testing.T) []transfer.Entry {
	t.Helper()
	got, err := r.ledger.Entries()
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func entry(id string, amount int64, to string) transfer.Entry {
	return transfer.Entry{ID: id, Amount: amount, Currency: transfer.Currency, To: to}
}

// The baseline has no protection, so every injected fault changes the
// effect or what the agent believes. These tests pin that behavior as the
// comparison point for the SAGE run.
func TestBaselineScenarios(t *testing.T) {
	t.Run(string(s.Normal), func(t *testing.T) {
		r := start(t, relay.Pass{})
		out, err := r.call(t, s.Approved)
		if err != nil || string(out) != `{"balance":990,"transfer_id":"t-000001"}` {
			t.Fatalf("call = %s, %v", out, err)
		}
		if got := r.entries(t); !reflect.DeepEqual(got, []transfer.Entry{entry("t-000001", 10, s.Recipient)}) {
			t.Fatalf("ledger = %+v", got)
		}
	})

	t.Run(string(s.TamperArgs), func(t *testing.T) {
		r := start(t, relay.FaultFunc(func(_ int, dir relay.Direction, i int, f []byte) []byte {
			if dir == relay.Request && i == RequestCall {
				return TamperArguments(f, s.Substituted)
			}
			return f
		}))
		out, err := r.call(t, s.Approved)
		if err != nil || string(out) != `{"balance":10,"transfer_id":"t-000001"}` {
			t.Fatalf("call = %s, %v", out, err)
		}
		if got := r.entries(t); !reflect.DeepEqual(got, []transfer.Entry{entry("t-000001", 990, s.Attacker)}) {
			t.Fatalf("ledger = %+v", got)
		}
	})

	t.Run(string(s.Replay), func(t *testing.T) {
		r := start(t, relay.Pass{})
		if _, err := r.call(t, s.Approved); err != nil {
			t.Fatal(err)
		}
		answers, err := relay.Replay(context.Background(), r.host, r.relay.Records()[0].Requests, 500*time.Millisecond)
		if err != nil || len(answers) != 2 || !bytes.Contains(answers[ResponseCall], []byte(`"transfer_id":"t-000002"`)) {
			t.Fatalf("replay answers = %q, %v", answers, err)
		}
		want := []transfer.Entry{entry("t-000001", 10, s.Recipient), entry("t-000002", 10, s.Recipient)}
		if got := r.entries(t); !reflect.DeepEqual(got, want) {
			t.Fatalf("ledger = %+v", got)
		}
	})

	t.Run(string(s.Unapproved), func(t *testing.T) {
		r := start(t, relay.Pass{})
		if _, err := r.call(t, s.Substituted); err != nil {
			t.Fatal(err)
		}
		if got := r.entries(t); !reflect.DeepEqual(got, []transfer.Entry{entry("t-000001", 990, s.Attacker)}) {
			t.Fatalf("ledger = %+v", got)
		}
	})

	t.Run(string(s.ForgeResult), func(t *testing.T) {
		r := start(t, relay.FaultFunc(func(_ int, dir relay.Direction, i int, f []byte) []byte {
			if dir == relay.Response && i == ResponseCall {
				return ForgeResult(f, s.ForgedResult)
			}
			return f
		}))
		out, err := r.call(t, s.Approved)
		if err != nil || !bytes.Equal(out, s.ForgedResult) {
			t.Fatalf("call = %s, %v", out, err)
		}
		if got := r.entries(t); !reflect.DeepEqual(got, []transfer.Entry{entry("t-000001", 10, s.Recipient)}) {
			t.Fatalf("ledger = %+v", got)
		}
	})
}

func TestHostRefusals(t *testing.T) {
	r := start(t, relay.Pass{})
	if _, err := r.call(t, []byte(`{"amount":2000,"currency":"DEMO","to":"did:sage:web:agent.example:carol"}`)); !errors.Is(err, ErrTool) {
		t.Fatalf("invalid transfer error = %v, want ErrTool", err)
	}

	unknown := start(t, relay.FaultFunc(func(_ int, dir relay.Direction, i int, f []byte) []byte {
		if dir == relay.Request && i == RequestCall {
			return bytes.Replace(f, []byte(`"name":"transfer"`), []byte(`"name":"other"`), 1)
		}
		return f
	}))
	if _, err := unknown.call(t, s.Approved); !errors.Is(err, ErrTool) {
		t.Fatalf("unknown tool error = %v, want ErrTool", err)
	}

	for name, frame := range map[string]string{
		"not json":       `{`,
		"wrong version":  `{"jsonrpc":"1.0","id":1,"method":"initialize"}`,
		"unknown method": `{"jsonrpc":"2.0","id":1,"method":"resources/list"}`,
	} {
		conn, err := net.DialTimeout("tcp", r.host, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		_ = relay.WriteFrame(conn, []byte(frame))
		if _, err := relay.ReadFrame(conn); err == nil {
			t.Errorf("%s: host answered", name)
		}
		_ = conn.Close()
	}
	if got := r.entries(t); len(got) != 0 {
		t.Fatalf("ledger = %+v", got)
	}
}

func TestCallRefusesUnexpectedAnswers(t *testing.T) {
	answers := map[string][]string{
		"initialize error":  {`{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"no"}}`},
		"wrong id":          {`{"jsonrpc":"2.0","id":7,"result":{}}`},
		"not json":          {`{`},
		"result not object": {`{"jsonrpc":"2.0","id":1,"result":{}}`, `{"jsonrpc":"2.0","id":2,"result":[]}`},
		"no structured":     {`{"jsonrpc":"2.0","id":1,"result":{}}`, `{"jsonrpc":"2.0","id":2,"result":{"content":[],"isError":false}}`},
	}
	for name, script := range answers {
		t.Run(name, func(t *testing.T) {
			agent, host := net.Pipe()
			defer func() { _ = agent.Close() }()
			go answer(host, script)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if _, err := Call(ctx, agent, s.Approved); err == nil {
				t.Fatal("Call accepted an unexpected answer")
			}
		})
	}
}

// answer plays a scripted host: it answers each request that carries an id
// with the next scripted frame and skips notifications.
func answer(host net.Conn, script []string) {
	defer func() { _ = host.Close() }()
	for _, a := range script {
		for {
			frame, err := relay.ReadFrame(host)
			if err != nil {
				return
			}
			if bytes.Contains(frame, []byte(`"id"`)) {
				break
			}
		}
		if relay.WriteFrame(host, []byte(a)) != nil {
			return
		}
	}
	_, _ = relay.ReadFrame(host)
}

func TestNewHostRefusesInvalidConfiguration(t *testing.T) {
	if _, err := NewHost(nil, time.Second); err == nil {
		t.Fatal("NewHost(nil) succeeded")
	}
	ledger, err := transfer.OpenLedger(filepath.Join(t.TempDir(), "l"), 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewHost(ledger, 0); err == nil {
		t.Fatal("NewHost with no timeout succeeded")
	}
}

func TestFaultHelpersLeaveOtherFramesUnchanged(t *testing.T) {
	for _, f := range []string{`{`, `{"method":"initialize"}`, `{"method":"tools/call","params":[]}`} {
		if got := TamperArguments([]byte(f), s.Substituted); string(got) != f {
			t.Errorf("TamperArguments(%s) = %s", f, got)
		}
	}
	for _, f := range []string{`{`, `{"result":{"protocolVersion":"x"}}`, `{"result":[]}`} {
		if got := ForgeResult([]byte(f), s.ForgedResult); string(got) != f {
			t.Errorf("ForgeResult(%s) = %s", f, got)
		}
	}
}
