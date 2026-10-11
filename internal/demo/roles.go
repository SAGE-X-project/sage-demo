// SPDX-License-Identifier: LGPL-3.0-or-later
package demo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/sage-x-project/sage-adk/core/guardhost"

	"github.com/sage-x-project/sage-demo/internal/baseline"
	"github.com/sage-x-project/sage-demo/internal/protected"
	s "github.com/sage-x-project/sage-demo/internal/scenario"
	"github.com/sage-x-project/sage-demo/internal/transfer"
)

const (
	hostTimeout = 5 * time.Second
	callTimeout = 30 * time.Second
)

// ToolHostConfig selects one tool host process.
type ToolHostConfig struct {
	Dir    string
	Mode   Mode
	Ledger string
	// Create opens new SAGE host state; only setup sets it.
	Create bool
}

// AgentConfig selects one agent process.
type AgentConfig struct {
	Dir      string
	Mode     Mode
	Target   string
	Scenario s.Name
	Calls    int
	// Create opens new SAGE host state; only setup sets it.
	Create bool
	// Wait delays the first call, so that setup's warm-up call follows the
	// replay quarantine.
	Wait time.Duration
}

// CallOutcome is what the agent saw for one call.
type CallOutcome struct {
	Result    json.RawMessage `json:"result,omitempty"`
	Error     string          `json:"error,omitempty"`
	ElapsedMS float64         `json:"elapsed_ms"`
}

// Outcome is the agent process result.
type Outcome struct {
	Calls    []CallOutcome `json:"calls"`
	WaitedMS float64       `json:"waited_ms,omitempty"`
}

// Sleep waits for d or until ctx ends.
func Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func options(dir string, create bool, clock guardhost.Clock) protected.Options {
	return protected.Options{Dir: dir, Create: create, Clock: clock, SignerUID: uint32(os.Getuid()), Timeout: hostTimeout}
}

// ServeSigner serves role's keys until ctx ends. Only processes of the
// current account may use the signer.
func ServeSigner(ctx context.Context, dir string, role protected.Role, ready func()) error {
	return protected.ServeSigner(ctx, options(dir, false, nil), role, uint32(os.Getuid()), ready)
}

// ServeToolHost opens the ledger and the tool host of the mode, listens on a
// loopback port, reports its address and serves until ctx ends.
func ServeToolHost(ctx context.Context, c ToolHostConfig, clock guardhost.Clock, ready func(addr string)) (err error) {
	ledger, err := transfer.OpenLedger(c.Ledger, s.OpeningBalance)
	if err != nil {
		return err
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	switch c.Mode {
	case Baseline:
		h, err := baseline.NewHost(ledger, hostTimeout)
		if err != nil {
			_ = l.Close()
			return err
		}
		ready(l.Addr().String())
		return h.Serve(ctx, l)
	case Sage:
		h, err := protected.OpenToolHost(ctx, options(c.Dir, c.Create, clock), ledger)
		if err != nil {
			_ = l.Close()
			return err
		}
		ready(l.Addr().String())
		serr := h.Serve(ctx, l)
		closing, cancel := context.WithTimeout(context.Background(), hostTimeout)
		defer cancel()
		return errors.Join(serr, h.Close(closing))
	}
	_ = l.Close()
	return fmt.Errorf("unknown mode %q", c.Mode)
}

// RunAgent makes the scenario's calls to target, each on a new connection,
// and returns what the agent saw. A refused call is an outcome, not an error;
// an error means the agent could not run.
func RunAgent(ctx context.Context, c AgentConfig, clock guardhost.Clock, sleep func(context.Context, time.Duration) error) (out Outcome, err error) {
	arguments := Requested(c.Scenario)
	var call func(context.Context, net.Conn) ([]byte, error)
	switch c.Mode {
	case Baseline:
		call = func(ctx context.Context, conn net.Conn) ([]byte, error) { return baseline.Call(ctx, conn, arguments) }
	case Sage:
		a, err := protected.OpenAgent(ctx, options(c.Dir, c.Create, clock))
		if err != nil {
			return Outcome{}, err
		}
		defer func() { err = errors.Join(err, a.Close()) }()
		call = func(ctx context.Context, conn net.Conn) ([]byte, error) { return a.Call(ctx, conn, arguments) }
	default:
		return Outcome{}, fmt.Errorf("unknown mode %q", c.Mode)
	}
	if c.Wait > 0 {
		start := time.Now()
		if err := sleep(ctx, c.Wait); err != nil {
			return Outcome{}, err
		}
		out.WaitedMS = millis(time.Since(start))
	}
	out.Calls = []CallOutcome{}
	for range c.Calls {
		out.Calls = append(out.Calls, once(ctx, c.Target, call))
	}
	return out, nil
}

func once(ctx context.Context, target string, call func(context.Context, net.Conn) ([]byte, error)) CallOutcome {
	start := time.Now()
	conn, err := net.DialTimeout("tcp", target, hostTimeout)
	if err != nil {
		return CallOutcome{Error: err.Error(), ElapsedMS: millis(time.Since(start))}
	}
	defer func() { _ = conn.Close() }()
	cctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	result, err := call(cctx, conn)
	o := CallOutcome{ElapsedMS: millis(time.Since(start))}
	if err != nil {
		o.Error = err.Error()
	} else {
		o.Result = json.RawMessage(result)
	}
	return o
}

func millis(d time.Duration) float64 {
	return float64(d.Microseconds()/10) / 100
}
