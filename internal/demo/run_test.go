// SPDX-License-Identifier: LGPL-3.0-or-later
package demo

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sage-x-project/sage-demo/internal/protected"
	s "github.com/sage-x-project/sage-demo/internal/scenario"
)

// Every scenario in both modes, through the relay, with real cryptographic
// exchanges in the SAGE mode. Setup waits out the quarantine by advancing the
// shared test clock, then each SAGE run reopens the non-empty host state.
func TestRunsMatchTheDesign(t *testing.T) {
	dir := shortDir(t)
	runner := Runner{Launcher: local{clock: &testClock{started: time.Now()}}}
	setup, err := runner.Setup(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if setup.Setup == nil || setup.Setup.QuarantineWaitMS < 0 || !setup.Accepted || setup.Name != SetupName {
		t.Fatalf("setup report = %+v", setup)
	}
	if _, err := runner.Setup(context.Background(), dir); err == nil {
		t.Fatal("setup reused a directory")
	}

	for _, m := range []Mode{Baseline, Sage} {
		for _, n := range s.All {
			rep, err := runner.Run(context.Background(), Config{Dir: dir, Mode: m, Scenario: n, Calls: 1})
			if err != nil {
				t.Fatalf("%s %s: %v", m, n, err)
			}
			if !rep.AsExpected {
				t.Errorf("%s %s differs from the design: %q", m, n, rep.Mismatches)
			}
			checkRun(t, rep)
		}
	}

	rep, err := runner.Run(context.Background(), Config{Dir: dir, Mode: Sage, Scenario: s.Normal, Calls: 3})
	if err != nil || len(rep.Calls) != 3 || len(rep.Ledger) != 3 || rep.Harmed() || rep.Latency.MinMS > rep.Latency.MaxMS {
		t.Fatalf("repeated calls = %+v, %v", rep, err)
	}

	reports, err := ReadReports(dir)
	if err != nil || len(reports) != 12 {
		t.Fatalf("ReadReports() = %d reports, %v", len(reports), err)
	}
	var out bytes.Buffer
	if err := Compare(&out, reports); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"tamper-args", "HARMED", "sage normal latency", "over 3 calls", "sage setup:", "not a conformance verdict"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("compare output lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "DIFFERS") || strings.Contains(out.String(), "not run") {
		t.Errorf("compare output:\n%s", out.String())
	}
}

// checkRun pins what each scenario shows beyond the expectation.
func checkRun(t *testing.T, rep Report) {
	t.Helper()
	frames := Frames{}
	if len(rep.Frames) > 0 {
		frames = rep.Frames[0]
	}
	switch {
	case rep.Mode == Sage && rep.Scenario == s.TamperArgs:
		// The exchange reached the changed frame, so the refusal follows the
		// change rather than a broken setup.
		if frames.Requests <= protected.RequestCall || frames.Responses < protected.RequestCall || len(rep.Ledger) != 0 {
			t.Errorf("sage tamper-args: frames %+v, ledger %+v", frames, rep.Ledger)
		}
	case rep.Mode == Sage && rep.Scenario == s.Unapproved:
		if frames.Requests != 0 || len(rep.Ledger) != 0 {
			t.Errorf("sage unapproved: frames %+v, ledger %+v", frames, rep.Ledger)
		}
	case rep.Scenario == s.Replay:
		want := map[Mode]int{Baseline: 2, Sage: 0}[rep.Mode]
		if rep.Replay == nil || rep.Replay.Answered != want {
			t.Errorf("%s replay: %+v", rep.Mode, rep.Replay)
		}
	case rep.Mode == Sage && rep.Scenario == s.ForgeResult:
		if len(rep.Ledger) != 1 || rep.Calls[0].Error == "" {
			t.Errorf("sage forge-result: %+v", rep)
		}
	}
	if rep.Mode == Sage && len(rep.Notes) < 3 || rep.Revisions.Spec != Spec {
		t.Errorf("%s %s: notes %q, revisions %+v", rep.Mode, rep.Scenario, rep.Notes, rep.Revisions)
	}
}

func TestRunRefusesInvalidConfiguration(t *testing.T) {
	dir := shortDir(t)
	runner := Runner{Launcher: local{clock: &testClock{started: time.Now()}}}
	for name, c := range map[string]Config{
		"no directory":        {Mode: Baseline, Scenario: s.Normal, Calls: 1},
		"unknown mode":        {Dir: dir, Mode: "other", Scenario: s.Normal, Calls: 1},
		"unknown scenario":    {Dir: dir, Mode: Baseline, Scenario: "other", Calls: 1},
		"no calls":            {Dir: dir, Mode: Baseline, Scenario: s.Normal},
		"too many calls":      {Dir: dir, Mode: Baseline, Scenario: s.Normal, Calls: MaxCalls + 1},
		"repeated fault call": {Dir: dir, Mode: Baseline, Scenario: s.Replay, Calls: 2},
		"sage without setup":  {Dir: dir, Mode: Sage, Scenario: s.Normal, Calls: 1},
	} {
		if _, err := runner.Run(context.Background(), c); err == nil {
			t.Errorf("%s: Run succeeded", name)
		}
	}
}

func TestSetupFailsWhenTheWarmUpCallIsRefused(t *testing.T) {
	dir := shortDir(t)
	// A one-millisecond wait leaves the new state inside its quarantine.
	runner := Runner{Launcher: local{clock: &testClock{started: time.Now()}}, Wait: time.Millisecond}
	rep, err := runner.Setup(context.Background(), dir)
	if err == nil || rep.Accepted || !strings.Contains(err.Error(), "run setup again") {
		t.Fatalf("Setup() = %+v, %v", rep, err)
	}
	if _, err := os.Stat(ReportPath(dir, SetupName)); err != nil {
		t.Fatalf("the refused setup report was not kept: %v", err)
	}
	// A failed setup does not unlock SAGE runs.
	if _, err := runner.Run(context.Background(), Config{Dir: dir, Mode: Sage, Scenario: s.Normal, Calls: 1}); err == nil || !strings.Contains(err.Error(), "run demo setup first") {
		t.Fatalf("Run after a failed setup = %v", err)
	}
}
