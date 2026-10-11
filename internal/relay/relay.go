// SPDX-License-Identifier: LGPL-3.0-or-later

// Package relay is the demo's intermediary between the agent and the tool
// host. It forwards length-framed messages on loopback and can change a frame
// or record a connection for a later resend. It is a fault injector for the
// demo's own processes, not a tool for other systems.
package relay

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

// MaxFrame is the largest frame payload, the same limit as the core's native
// MCP stream.
const MaxFrame = 32768

// ErrFrame refuses an empty or oversized frame.
var ErrFrame = errors.New("relay: invalid frame")

// Direction names which way a frame travels.
type Direction int

const (
	// Request frames travel from the agent to the tool host.
	Request Direction = iota
	// Response frames travel from the tool host to the agent.
	Response
)

// Fault decides what the relay forwards. Frame receives each frame with its
// zero-based index in that direction of the connection and returns the bytes
// to forward. It must not keep or modify its argument. Returning an empty
// slice ends the connection.
type Fault interface {
	Frame(conn int, dir Direction, index int, frame []byte) []byte
}

// Pass forwards every frame unchanged.
type Pass struct{}

// Frame returns the frame unchanged.
func (Pass) Frame(_ int, _ Direction, _ int, frame []byte) []byte { return frame }

// FaultFunc adapts a function to Fault.
type FaultFunc func(conn int, dir Direction, index int, frame []byte) []byte

// Frame calls f.
func (f FaultFunc) Frame(conn int, dir Direction, index int, frame []byte) []byte {
	return f(conn, dir, index, frame)
}

// Flip returns a copy of frame with the byte at offset inverted. An offset
// past the end changes the last byte.
func Flip(frame []byte, offset int) []byte {
	out := append([]byte(nil), frame...)
	if len(out) == 0 {
		return out
	}
	if offset < 0 || offset >= len(out) {
		offset = len(out) - 1
	}
	out[offset] ^= 0xff
	return out
}

// ReadFrame reads one frame: a 4-byte big-endian length and the payload.
func ReadFrame(r io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(header[:])
	if n == 0 || n > MaxFrame {
		return nil, ErrFrame
	}
	frame := make([]byte, n)
	if _, err := io.ReadFull(r, frame); err != nil {
		return nil, err
	}
	return frame, nil
}

// WriteFrame writes one frame.
func WriteFrame(w io.Writer, frame []byte) error {
	if len(frame) == 0 || len(frame) > MaxFrame {
		return ErrFrame
	}
	out := make([]byte, 4+len(frame))
	binary.BigEndian.PutUint32(out, uint32(len(frame)))
	copy(out[4:], frame)
	_, err := w.Write(out)
	return err
}

// Record holds the frames one connection carried after the fault was applied.
type Record struct {
	Requests, Responses [][]byte
}

// Relay accepts agent connections and forwards each to the target.
type Relay struct {
	listener net.Listener
	target   string
	fault    Fault
	timeout  time.Duration

	mu      sync.Mutex
	records []*Record
	wg      sync.WaitGroup
}

// Listen opens a relay on addr that forwards to target. The timeout bounds
// dialing the target.
func Listen(addr, target string, fault Fault, timeout time.Duration) (*Relay, error) {
	if fault == nil || target == "" || timeout <= 0 {
		return nil, errors.New("relay: invalid configuration")
	}
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	return &Relay{listener: l, target: target, fault: fault, timeout: timeout}, nil
}

// Addr returns the address agents dial.
func (r *Relay) Addr() string { return r.listener.Addr().String() }

// Serve forwards connections until ctx ends or the listener closes, then
// waits for open connections to finish.
func (r *Relay) Serve(ctx context.Context) error {
	stop := context.AfterFunc(ctx, func() { _ = r.listener.Close() })
	defer stop()
	defer r.wg.Wait()
	for {
		conn, err := r.listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		rec := &Record{}
		r.mu.Lock()
		index := len(r.records)
		r.records = append(r.records, rec)
		r.mu.Unlock()
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			r.forward(ctx, index, rec, conn)
		}()
	}
}

// Close stops accepting connections.
func (r *Relay) Close() error { return r.listener.Close() }

// Records returns copies of the connection records in accept order.
func (r *Relay) Records() []Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Record, len(r.records))
	for i, rec := range r.records {
		out[i] = Record{Requests: clone(rec.Requests), Responses: clone(rec.Responses)}
	}
	return out
}

func (r *Relay) forward(ctx context.Context, index int, rec *Record, agent net.Conn) {
	defer func() { _ = agent.Close() }()
	d := net.Dialer{Timeout: r.timeout}
	tool, err := d.DialContext(ctx, "tcp", r.target)
	if err != nil {
		return
	}
	defer func() { _ = tool.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = agent.Close(); _ = tool.Close() })
	defer stop()
	done := make(chan struct{}, 2)
	go func() { r.pump(index, Request, agent, tool, rec); done <- struct{}{} }()
	go func() { r.pump(index, Response, tool, agent, rec); done <- struct{}{} }()
	<-done
	_ = agent.Close()
	_ = tool.Close()
	<-done
}

func (r *Relay) pump(index int, dir Direction, src, dst net.Conn, rec *Record) {
	for i := 0; ; i++ {
		frame, err := ReadFrame(src)
		if err != nil {
			return
		}
		out := r.fault.Frame(index, dir, i, frame)
		r.mu.Lock()
		if dir == Request {
			rec.Requests = append(rec.Requests, append([]byte(nil), out...))
		} else {
			rec.Responses = append(rec.Responses, append([]byte(nil), out...))
		}
		r.mu.Unlock()
		if WriteFrame(dst, out) != nil {
			return
		}
	}
}

// Replay dials target on a new connection, sends the recorded request frames
// and collects response frames until the target closes the connection or the
// timeout passes. It reports what the target answered; an error means the
// resend itself could not be made.
func Replay(ctx context.Context, target string, requests [][]byte, timeout time.Duration) ([][]byte, error) {
	if len(requests) == 0 || timeout <= 0 {
		return nil, errors.New("relay: nothing to replay")
	}
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", target)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return nil, err
	}
	go func() {
		for _, f := range requests {
			if WriteFrame(conn, f) != nil {
				return
			}
		}
	}()
	responses := [][]byte{}
	for {
		frame, err := ReadFrame(conn)
		if err != nil {
			return responses, nil
		}
		responses = append(responses, frame)
	}
}

func clone(frames [][]byte) [][]byte {
	out := make([][]byte, len(frames))
	for i, f := range frames {
		out[i] = append([]byte(nil), f...)
	}
	return out
}
