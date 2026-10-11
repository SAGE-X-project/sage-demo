// SPDX-License-Identifier: LGPL-3.0-or-later
package demo

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"

	s "github.com/sage-x-project/sage-demo/internal/scenario"
	"github.com/sage-x-project/sage-demo/internal/transfer"
)

// Spec is the frozen normative sage-spec revision the pinned modules follow.
const Spec = "1820ab5eafb843e1c13f4c46c34aeeb28d934ac9"

// Revisions names what a report was produced with.
type Revisions struct {
	Spec string `json:"spec"`
	ADK  string `json:"adk"`
	Core string `json:"core"`
	Demo string `json:"demo,omitempty"`
	Go   string `json:"go"`
}

func revisions() Revisions {
	r := Revisions{Spec: Spec}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return r
	}
	r.Go = info.GoVersion
	for _, d := range info.Deps {
		switch d.Path {
		case "github.com/sage-x-project/sage-adk":
			r.ADK = d.Version
		case "github.com/sage-x-project/sage":
			r.Core = d.Version
		}
	}
	dirty := false
	for _, kv := range info.Settings {
		switch kv.Key {
		case "vcs.revision":
			r.Demo = kv.Value
		case "vcs.modified":
			dirty = kv.Value == "true"
		}
	}
	if dirty && r.Demo != "" {
		r.Demo += "+modified"
	}
	return r
}

// Frames counts the frames the relay forwarded on one connection.
type Frames struct {
	Requests  int `json:"requests"`
	Responses int `json:"responses"`
}

// FaultChange names one frame the relay's fault changed.
type FaultChange struct {
	Direction string `json:"direction"`
	Index     int    `json:"index"`
}

// ReplayOutcome is what the tool host answered to the resent connection.
type ReplayOutcome struct {
	Answered int `json:"answered_frames"`
}

// Latency summarizes the call times of a run.
type Latency struct {
	MinMS    float64 `json:"min_ms"`
	MedianMS float64 `json:"median_ms"`
	MaxMS    float64 `json:"max_ms"`
}

// SetupCost is the one-time cost of SAGE setup.
type SetupCost struct {
	QuarantineWaitMS float64 `json:"quarantine_wait_ms"`
	TotalMS          float64 `json:"total_ms"`
}

// Report is the result of one run.
type Report struct {
	Name       string           `json:"name"`
	Mode       Mode             `json:"mode"`
	Scenario   s.Name           `json:"scenario"`
	Started    string           `json:"started"`
	Revisions  Revisions        `json:"revisions"`
	Requested  json.RawMessage  `json:"requested"`
	Calls      []CallOutcome    `json:"calls"`
	Accepted   bool             `json:"accepted"`
	Frames     []Frames         `json:"relay_frames"`
	Changed    []FaultChange    `json:"fault_changed"`
	Replay     *ReplayOutcome   `json:"replay,omitempty"`
	Ledger     []transfer.Entry `json:"ledger"`
	Harms      []string         `json:"harms"`
	Expected   Expectation      `json:"expected"`
	AsExpected bool             `json:"as_expected"`
	Mismatches []string         `json:"mismatches"`
	Latency    Latency          `json:"latency"`
	Setup      *SetupCost       `json:"setup,omitempty"`
	Notes      []string         `json:"notes"`
}

var notes = map[Mode][]string{
	Baseline: {
		"Unprotected MCP JSON-RPC over loopback TCP; the agent cannot check who answered or whether bytes changed.",
	},
	Sage: {
		"The Registry Source is a local file whose readiness flags are asserted; it observes no chain.",
		"The tool measurement accepts any snapshot.",
		"All processes run as one account on one kernel.",
		"The host returns one error code for every refusal; the report does not claim a refusal stage.",
		"A demo run is not a conformance verdict or deployment evidence.",
	},
}

// Harmed reports whether the run has any harm.
func (r Report) Harmed() bool { return len(r.Harms) > 0 }

// assess lists the harms the design defines: the ledger holds a transfer the
// agent did not ask for or outside the approved operation, holds more
// transfers than the agent made calls, or the agent accepted a result that
// matches no ledger state after its own transfer. Accepted results must match
// ledger states in call order, so a result swapped between calls is a harm.
func assess(requested []byte, calls []CallOutcome, ledger []transfer.Entry) []string {
	harms := []string{}
	want, _ := transfer.Parse(requested)
	approved, _ := transfer.Parse(s.Approved)
	states := []transfer.Result{}
	balance := int64(s.OpeningBalance)
	for _, e := range ledger {
		got := transfer.Request{Amount: e.Amount, Currency: e.Currency, To: e.To}
		if got != want {
			harms = append(harms, fmt.Sprintf("%s: transferred %d to %s, which the agent did not ask for", e.ID, e.Amount, e.To))
		}
		if got != approved {
			harms = append(harms, fmt.Sprintf("%s: transferred %d to %s, outside the approved operation", e.ID, e.Amount, e.To))
		}
		balance -= e.Amount
		states = append(states, transfer.Result{Balance: balance, TransferID: e.ID})
	}
	if len(ledger) > len(calls) {
		harms = append(harms, fmt.Sprintf("the ledger holds %d transfers for %d agent calls", len(ledger), len(calls)))
	}
	next := 0
	for i, c := range calls {
		if c.Error != "" {
			continue
		}
		var r transfer.Result
		d := json.NewDecoder(bytes.NewReader(c.Result))
		d.DisallowUnknownFields()
		found := d.Decode(&r) == nil && slices.Contains(states[min(next, len(states)):], r)
		if found {
			next += slices.Index(states[next:], r) + 1
		} else {
			harms = append(harms, fmt.Sprintf("call %d: the agent accepted %s, which matches no ledger state after its transfer", i+1, c.Result))
		}
	}
	return harms
}

func latency(calls []CallOutcome) Latency {
	times := make([]float64, len(calls))
	for i, c := range calls {
		times[i] = c.ElapsedMS
	}
	slices.Sort(times)
	if len(times) == 0 {
		return Latency{}
	}
	return Latency{MinMS: times[0], MedianMS: times[len(times)/2], MaxMS: times[len(times)-1]}
}

func reportDir(dir string) string { return filepath.Join(dir, "reports") }

// ReportPath returns the file of the named report.
func ReportPath(dir, name string) string { return filepath.Join(reportDir(dir), name+".json") }

func writeReport(dir string, r Report) error {
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(ReportPath(dir, r.Name), append(raw, '\n'), 0o600)
}

// ReadReports reads every report under dir, setup included, in name order.
func ReadReports(dir string) ([]Report, error) {
	paths, err := filepath.Glob(filepath.Join(reportDir(dir), "*.json"))
	if err != nil {
		return nil, err
	}
	slices.Sort(paths)
	out := []Report{}
	for _, p := range paths {
		r, err := readReport(p)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func readReport(path string) (Report, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Report{}, err
	}
	var r Report
	if err := json.Unmarshal(raw, &r); err != nil {
		return Report{}, fmt.Errorf("%s: %w", path, err)
	}
	return r, nil
}
