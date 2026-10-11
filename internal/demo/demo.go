// SPDX-License-Identifier: LGPL-3.0-or-later

// Package demo runs the comparison: it starts the role processes of one mode,
// puts the relay between the agent and the tool host, injects the scenario's
// fault, and reports what the agent saw and what the ledger holds.
package demo

import (
	"fmt"
	"slices"

	"github.com/sage-x-project/sage-demo/internal/baseline"
	"github.com/sage-x-project/sage-demo/internal/protected"
	"github.com/sage-x-project/sage-demo/internal/relay"
	s "github.com/sage-x-project/sage-demo/internal/scenario"
)

// Mode selects the unprotected or the SAGE run.
type Mode string

const (
	// Baseline is unprotected MCP JSON-RPC.
	Baseline Mode = "baseline"
	// Sage is the sage-adk guardhost caller and receiver.
	Sage Mode = "sage"
)

// ParseMode returns the mode with the given name.
func ParseMode(m string) (Mode, error) {
	switch Mode(m) {
	case Baseline, Sage:
		return Mode(m), nil
	}
	return "", fmt.Errorf("unknown mode %q", m)
}

// MaxCalls bounds the calls of one run; each approved transfer moves 10 of
// the opening balance of 1000.
const MaxCalls = 50

// Config selects one run.
type Config struct {
	Dir      string
	Mode     Mode
	Scenario s.Name
	// Calls is the number of agent calls; more than one only for normal,
	// where it measures latency.
	Calls int
}

func (c Config) validate() error {
	if c.Dir == "" {
		return fmt.Errorf("no demo directory")
	}
	if _, err := ParseMode(string(c.Mode)); err != nil {
		return err
	}
	if _, err := s.Parse(string(c.Scenario)); err != nil {
		return err
	}
	if c.Calls < 1 || c.Calls > MaxCalls || (c.Calls > 1 && c.Scenario != s.Normal) {
		return fmt.Errorf("calls must be 1, or 1 to %d for %s", MaxCalls, s.Normal)
	}
	return nil
}

// Requested returns the transfer the agent asks for in a scenario.
func Requested(n s.Name) []byte {
	if n == s.Unapproved {
		return s.Substituted
	}
	return s.Approved
}

// Fault returns the relay behavior of a scenario in a mode. The same fault is
// applied at the same point of the connection in both modes; the SAGE
// exchange is encrypted and signed, so there the relay changes bytes of the
// frame that carries the call or the result instead of rewriting fields.
func Fault(m Mode, n s.Name) relay.Fault {
	switch n {
	case s.TamperArgs:
		return relay.FaultFunc(func(_ int, dir relay.Direction, i int, f []byte) []byte {
			switch {
			case dir != relay.Request:
				return f
			case m == Baseline && i == baseline.RequestCall:
				return baseline.TamperArguments(f, s.Substituted)
			case m == Sage && i == protected.RequestCall:
				return relay.Flip(f, len(f)/2)
			}
			return f
		})
	case s.ForgeResult:
		return relay.FaultFunc(func(_ int, dir relay.Direction, i int, f []byte) []byte {
			switch {
			case dir != relay.Response:
				return f
			case m == Baseline && i == baseline.ResponseCall:
				return baseline.ForgeResult(f, s.ForgedResult)
			case m == Sage && i >= protected.ResponseResult:
				return relay.Flip(f, len(f)/2)
			}
			return f
		})
	}
	return relay.Pass{}
}

// Expectation is what the design expects of a scenario in a mode: whether
// every agent call is accepted and whether the run is harmed.
type Expectation struct {
	Accepted bool `json:"accepted"`
	Harmed   bool `json:"harmed"`
}

// Expected returns the design's expectation. Without protection every fault
// harms the run and the agent accepts what it receives. With SAGE no fault
// harms the run; the agent's own call is accepted only when nothing changed
// it (normal, and the original call of replay).
func Expected(m Mode, n s.Name) Expectation {
	if m == Baseline {
		return Expectation{Accepted: true, Harmed: n != s.Normal}
	}
	return Expectation{Accepted: n == s.Normal || n == s.Replay}
}

// mismatches lists where a run departs from the design. Besides the
// expectation, a fault scenario must show that the fault took effect where
// the design puts it, because the SAGE host returns one error code for every
// refusal and a refusal alone does not show which check stopped the call.
func mismatches(r Report) []string {
	out := []string{}
	if r.Accepted != r.Expected.Accepted {
		out = append(out, fmt.Sprintf("agent accepted every call: %v, expected %v", r.Accepted, r.Expected.Accepted))
	}
	if r.Harmed() != r.Expected.Harmed {
		out = append(out, fmt.Sprintf("harmed: %v, expected %v", r.Harmed(), r.Expected.Harmed))
	}
	first := Frames{}
	if len(r.Frames) > 0 {
		first = r.Frames[0]
	}
	changed := func(dir string) bool {
		return slices.ContainsFunc(r.Changed, func(c FaultChange) bool { return c.Direction == dir })
	}
	switch r.Scenario {
	case s.TamperArgs:
		if !changed("request") {
			out = append(out, "the fault changed no request frame")
		}
		if r.Mode == Sage && first.Responses < protected.RequestCall {
			out = append(out, fmt.Sprintf("the tool host answered %d frames, fewer than precede the call frame", first.Responses))
		}
	case s.ForgeResult:
		if !changed("response") {
			out = append(out, "the fault changed no response frame")
		}
		if len(r.Ledger) != 1 {
			out = append(out, fmt.Sprintf("the ledger holds %d transfers; the call should have run once", len(r.Ledger)))
		}
	case s.Replay:
		want := map[Mode]int{Baseline: 2, Sage: 0}[r.Mode]
		if r.Replay == nil || r.Replay.Answered != want {
			got := "no resend"
			if r.Replay != nil {
				got = fmt.Sprintf("%d answered frames", r.Replay.Answered)
			}
			out = append(out, fmt.Sprintf("the resent connection got %s, expected %d", got, want))
		}
	case s.Unapproved:
		if r.Mode == Sage && slices.ContainsFunc(r.Frames, func(f Frames) bool { return f.Requests > 0 }) {
			out = append(out, "an unapproved call reached the relay")
		}
	}
	return out
}
