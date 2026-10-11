// SPDX-License-Identifier: LGPL-3.0-or-later
package demo

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	s "github.com/sage-x-project/sage-demo/internal/scenario"
)

// Compare prints the latest report of each scenario in both modes side by
// side, the normal-call latency and the setup cost.
func Compare(w io.Writer, reports []Report) error {
	latest := map[Mode]map[s.Name]Report{Baseline: {}, Sage: {}}
	var setup *Report
	for _, r := range reports {
		if r.Name == SetupName {
			setup = &r
			continue
		}
		if m, ok := latest[r.Mode]; ok {
			// Names end in a fixed-width UTC timestamp.
			if prev, seen := m[r.Scenario]; !seen || prev.Name < r.Name {
				m[r.Scenario] = r
			}
		}
	}
	tw := tabwriter.NewWriter(w, 2, 8, 2, ' ', 0)
	t := &printer{w: tw}
	t.f("scenario\tbaseline\tsage\n")
	for _, n := range s.All {
		t.f("%s\t%s\t%s\n", n, cell(latest[Baseline], n), cell(latest[Sage], n))
	}
	if t.err != nil {
		return t.err
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	p := &printer{w: w}
	p.f("\n")
	for _, m := range []Mode{Baseline, Sage} {
		if r, ok := latest[m][s.Normal]; ok {
			p.f("%s normal latency: median %.2f ms, min %.2f ms, max %.2f ms over %d calls\n", m, r.Latency.MedianMS, r.Latency.MinMS, r.Latency.MaxMS, len(r.Calls))
		}
	}
	switch {
	case setup != nil && setupDone(*setup):
		p.f("sage setup: %.1f s in total, of which %.1f s waiting out the replay quarantine\n", setup.Setup.TotalMS/1000, setup.Setup.QuarantineWaitMS/1000)
	case setup != nil:
		p.f("sage setup failed; its cost is not shown\n")
	}
	p.f("\nThe SAGE run uses a synthetic Registry Source and an accept-all measurement on one account; it is not a conformance verdict.\n")
	return p.err
}

func cell(m map[s.Name]Report, n s.Name) string {
	r, ok := m[n]
	if !ok {
		return "not run"
	}
	var b strings.Builder
	if r.Accepted {
		b.WriteString("accepted")
	} else {
		b.WriteString("refused")
	}
	if r.Replay != nil {
		fmt.Fprintf(&b, ", resend answered %d frames", r.Replay.Answered)
	}
	fmt.Fprintf(&b, ", %d transfers", len(r.Ledger))
	if !r.Accepted && len(r.Ledger) > 0 {
		b.WriteString(" (ran, but the agent did not accept the result)")
	}
	if r.Harmed() {
		fmt.Fprintf(&b, ", HARMED (%d)", len(r.Harms))
	} else {
		b.WriteString(", no harm")
	}
	if !r.AsExpected {
		b.WriteString(", DIFFERS FROM EXPECTATION")
	}
	return b.String()
}
