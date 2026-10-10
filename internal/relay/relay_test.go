// SPDX-License-Identifier: LGPL-3.0-or-later
package relay

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

// target answers every request frame with "ok:" and the frame, and counts the
// frames it handled.
type target struct {
	listener net.Listener
	handled  atomic.Int64
}

func startTarget(t *testing.T) *target {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tg := &target{listener: l}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				for {
					frame, err := ReadFrame(conn)
					if err != nil {
						return
					}
					tg.handled.Add(1)
					if WriteFrame(conn, append([]byte("ok:"), frame...)) != nil {
						return
					}
				}
			}()
		}
	}()
	return tg
}

func startRelay(t *testing.T, targetAddr string, fault Fault) (*Relay, context.CancelFunc, chan error) {
	t.Helper()
	r, err := Listen("127.0.0.1:0", targetAddr, fault, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- r.Serve(ctx) }()
	t.Cleanup(func() { cancel(); <-served })
	return r, cancel, served
}

func exchange(t *testing.T, addr string, frames ...string) []string {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range frames {
		if err := WriteFrame(conn, []byte(f)); err != nil {
			t.Fatal(err)
		}
		answer, err := ReadFrame(conn)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, string(answer))
	}
	return got
}

func TestRelayPassesAndRecords(t *testing.T) {
	tg := startTarget(t)
	r, _, _ := startRelay(t, tg.listener.Addr().String(), Pass{})
	got := exchange(t, r.Addr(), "one", "two")
	if !reflect.DeepEqual(got, []string{"ok:one", "ok:two"}) {
		t.Fatalf("answers = %q", got)
	}
	waitRecords(t, r, 1, 2)
	rec := r.Records()[0]
	if string(rec.Requests[1]) != "two" || string(rec.Responses[0]) != "ok:one" {
		t.Fatalf("record = %q / %q", rec.Requests, rec.Responses)
	}
}

func TestRelayAppliesFaultByDirectionAndIndex(t *testing.T) {
	tg := startTarget(t)
	fault := FaultFunc(func(conn int, dir Direction, index int, frame []byte) []byte {
		switch {
		case conn == 0 && dir == Request && index == 1:
			return []byte("changed")
		case conn == 0 && dir == Response && index == 0:
			return bytes.ToUpper(frame)
		}
		return frame
	})
	r, _, _ := startRelay(t, tg.listener.Addr().String(), fault)
	got := exchange(t, r.Addr(), "one", "two")
	if !reflect.DeepEqual(got, []string{"OK:ONE", "ok:changed"}) {
		t.Fatalf("answers = %q", got)
	}
	if got := exchange(t, r.Addr(), "two"); got[0] != "ok:two" {
		t.Fatalf("second connection answer = %q", got)
	}
}

func TestReplayResendsRecordedRequests(t *testing.T) {
	tg := startTarget(t)
	r, _, _ := startRelay(t, tg.listener.Addr().String(), Pass{})
	exchange(t, r.Addr(), "pay")
	waitRecords(t, r, 1, 1)
	answers, err := Replay(context.Background(), tg.listener.Addr().String(), r.Records()[0].Requests, 300*time.Millisecond)
	if err != nil || len(answers) != 1 || string(answers[0]) != "ok:pay" {
		t.Fatalf("Replay() = %q, %v", answers, err)
	}
	if tg.handled.Load() != 2 {
		t.Fatalf("target handled %d frames, want 2", tg.handled.Load())
	}
	if _, err := Replay(context.Background(), tg.listener.Addr().String(), nil, time.Second); err == nil {
		t.Fatal("empty replay accepted")
	}
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := closed.Addr().String()
	_ = closed.Close()
	if _, err := Replay(context.Background(), addr, [][]byte{[]byte("x")}, 300*time.Millisecond); err == nil {
		t.Fatal("replay to a closed port succeeded")
	}
}

func TestRelayClosesAgentWhenTargetIsUnavailable(t *testing.T) {
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := closed.Addr().String()
	_ = closed.Close()
	r, _, _ := startRelay(t, addr, Pass{})
	conn, err := net.DialTimeout("tcp", r.Addr(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	_ = WriteFrame(conn, []byte("x"))
	if _, err := ReadFrame(conn); err == nil {
		t.Fatal("relay answered without a target")
	}
}

func TestServeStopsOnCancelAndClose(t *testing.T) {
	tg := startTarget(t)
	r, cancel, served := startRelay(t, tg.listener.Addr().String(), Pass{})
	conn, err := net.DialTimeout("tcp", r.Addr(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	cancel()
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("Serve() = %v", err)
		}
		served <- nil
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not stop with an open connection")
	}

	r2, err := Listen("127.0.0.1:0", tg.listener.Addr().String(), Pass{}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- r2.Serve(context.Background()) }()
	_ = r2.Close()
	if err := <-done; err == nil {
		t.Fatal("Serve() after Close returned nil")
	}
}

func TestListenRefusesInvalidConfiguration(t *testing.T) {
	for name, args := range map[string]struct {
		target  string
		fault   Fault
		timeout time.Duration
	}{
		"no target":  {"", Pass{}, time.Second},
		"no fault":   {"127.0.0.1:1", nil, time.Second},
		"no timeout": {"127.0.0.1:1", Pass{}, 0},
	} {
		if _, err := Listen("127.0.0.1:0", args.target, args.fault, args.timeout); err == nil {
			t.Errorf("%s: Listen() succeeded", name)
		}
	}
	if _, err := Listen("256.0.0.1:0", "127.0.0.1:1", Pass{}, time.Second); err == nil {
		t.Error("Listen() on an invalid address succeeded")
	}
}

func TestFrames(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, []byte("abc")); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadFrame(&buf); err != nil || string(got) != "abc" {
		t.Fatalf("ReadFrame() = %q, %v", got, err)
	}
	if err := WriteFrame(&buf, nil); !errors.Is(err, ErrFrame) {
		t.Fatalf("empty WriteFrame() = %v", err)
	}
	if err := WriteFrame(&buf, make([]byte, MaxFrame+1)); !errors.Is(err, ErrFrame) {
		t.Fatalf("oversized WriteFrame() = %v", err)
	}
	header := make([]byte, 4)
	binary.BigEndian.PutUint32(header, MaxFrame+1)
	if _, err := ReadFrame(bytes.NewReader(header)); !errors.Is(err, ErrFrame) {
		t.Fatalf("oversized ReadFrame() = %v", err)
	}
	if _, err := ReadFrame(bytes.NewReader([]byte{0, 0, 0, 0})); !errors.Is(err, ErrFrame) {
		t.Fatalf("empty ReadFrame() = %v", err)
	}
	if _, err := ReadFrame(bytes.NewReader([]byte{0, 0, 0, 5, 'a'})); err == nil {
		t.Fatal("truncated ReadFrame() succeeded")
	}
}

func TestFlip(t *testing.T) {
	in := []byte{1, 2, 3}
	if got := Flip(in, 1); !bytes.Equal(got, []byte{1, 0xfd, 3}) || in[1] != 2 {
		t.Fatalf("Flip(1) = %v, input %v", got, in)
	}
	if got := Flip(in, 9); !bytes.Equal(got, []byte{1, 2, 0xfc}) {
		t.Fatalf("Flip(past end) = %v", got)
	}
	if got := Flip(nil, 0); len(got) != 0 {
		t.Fatalf("Flip(nil) = %v", got)
	}
}

func waitRecords(t *testing.T, r *Relay, conns, responses int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		recs := r.Records()
		if len(recs) >= conns && len(recs[conns-1].Responses) >= responses {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("relay records incomplete")
}
