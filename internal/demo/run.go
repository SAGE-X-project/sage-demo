// SPDX-License-Identifier: LGPL-3.0-or-later
package demo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sage-x-project/sage-demo/internal/protected"
	"github.com/sage-x-project/sage-demo/internal/relay"
	s "github.com/sage-x-project/sage-demo/internal/scenario"
	"github.com/sage-x-project/sage-demo/internal/transfer"
)

// QuarantineWait is how long setup waits before its warm-up call: the core's
// 360-second replay quarantine and a margin.
const QuarantineWait = 365 * time.Second

// SetupName names the setup report and ledger.
const SetupName = "setup"

// Process is a started role process.
type Process interface {
	// Stop ends the process and waits for it. An error means it did not
	// exit cleanly.
	Stop() error
}

// Launcher starts the role processes of a run.
type Launcher interface {
	// Signer starts role's signer and returns once it accepts requests.
	Signer(ctx context.Context, dir string, role protected.Role) (Process, error)
	// ToolHost starts the tool host and returns once it listens at addr.
	ToolHost(ctx context.Context, c ToolHostConfig) (p Process, addr string, err error)
	// Agent runs the agent to completion.
	Agent(ctx context.Context, c AgentConfig) (Outcome, error)
}

// Runner runs scenarios with the processes its Launcher starts.
type Runner struct {
	Launcher Launcher
	// Wait is the setup quarantine wait; QuarantineWait when zero.
	Wait time.Duration
}

// Setup creates the SAGE demo directory, waits out the replay quarantine of
// the new host state and makes one warm-up call, so that later runs reopen
// non-empty state and start without waiting. A refused warm-up call leaves
// the state unusable; remove the directory and run setup again.
func (r Runner) Setup(ctx context.Context, dir string) (rep Report, err error) {
	start := time.Now()
	if err := protected.Setup(dir); err != nil {
		return Report{}, err
	}
	defer func() {
		if err != nil {
			err = fmt.Errorf("%w; remove %s and run setup again", err, dir)
		}
	}()
	unlock, err := lock(dir)
	if err != nil {
		return Report{}, err
	}
	defer unlock()
	if err := os.Mkdir(reportDir(dir), 0o700); err != nil {
		return Report{}, err
	}
	wait := r.Wait
	if wait == 0 {
		wait = QuarantineWait
	}
	rep, waited, err := r.execute(ctx, Config{Dir: dir, Mode: Sage, Scenario: s.Normal, Calls: 1}, SetupName, true, wait)
	if err != nil {
		return Report{}, err
	}
	rep.Setup = &SetupCost{QuarantineWaitMS: waited, TotalMS: millis(time.Since(start))}
	if err := writeReport(dir, rep); err != nil {
		return Report{}, err
	}
	if !setupDone(rep) {
		return rep, errors.New("setup warm-up call failed")
	}
	return rep, nil
}

func setupDone(r Report) bool {
	return r.Name == SetupName && r.Accepted && !r.Harmed() && r.Setup != nil
}

// Run runs one scenario and writes its report. A SAGE run needs a completed
// setup; a baseline run creates the directories it needs.
func (r Runner) Run(ctx context.Context, c Config) (Report, error) {
	if err := c.validate(); err != nil {
		return Report{}, err
	}
	if c.Mode == Sage {
		if setup, err := readReport(ReportPath(c.Dir, SetupName)); err != nil || !setupDone(setup) {
			return Report{}, fmt.Errorf("no completed setup in %s; run demo setup first", c.Dir)
		}
	}
	for _, d := range []string{c.Dir, reportDir(c.Dir), filepath.Dir(protected.Layout{Dir: c.Dir}.Ledger(SetupName))} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return Report{}, err
		}
	}
	unlock, err := lock(c.Dir)
	if err != nil {
		return Report{}, err
	}
	defer unlock()
	name := fmt.Sprintf("%s-%s-%s", c.Mode, c.Scenario, time.Now().UTC().Format("20060102T150405.000000000Z"))
	rep, _, err := r.execute(ctx, c, name, false, 0)
	if err != nil {
		return Report{}, err
	}
	return rep, writeReport(c.Dir, rep)
}

// execute starts the processes, runs the agent through the relay, resends
// the recorded connection in the replay scenario, stops every process and
// reads the ledger.
func (r Runner) execute(ctx context.Context, c Config, name string, create bool, wait time.Duration) (rep Report, waited float64, err error) {
	ledger := protected.Layout{Dir: c.Dir}.Ledger(name)
	if _, err := os.Stat(ledger); !errors.Is(err, os.ErrNotExist) {
		return Report{}, 0, fmt.Errorf("ledger %s already exists", ledger)
	}
	rep = Report{Name: name, Mode: c.Mode, Scenario: c.Scenario, Started: time.Now().UTC().Format(time.RFC3339Nano),
		Revisions: revisions(), Requested: Requested(c.Scenario), Expected: Expected(c.Mode, c.Scenario), Notes: notes[c.Mode]}

	var procs []Process
	stopAll := func() error {
		var errs error
		for i := len(procs) - 1; i >= 0; i-- {
			errs = errors.Join(errs, procs[i].Stop())
		}
		procs = nil
		return errs
	}
	defer func() { err = errors.Join(err, stopAll()) }()

	if c.Mode == Sage {
		for _, role := range []protected.Role{protected.Agent, protected.ToolHost} {
			p, err := r.Launcher.Signer(ctx, c.Dir, role)
			if err != nil {
				return Report{}, 0, fmt.Errorf("signer %s: %w", role, err)
			}
			procs = append(procs, p)
		}
	}
	host, addr, err := r.Launcher.ToolHost(ctx, ToolHostConfig{Dir: c.Dir, Mode: c.Mode, Ledger: ledger, Create: create})
	if err != nil {
		return Report{}, 0, fmt.Errorf("tool host: %w", err)
	}
	procs = append(procs, host)

	rl, err := relay.Listen("127.0.0.1:0", addr, Fault(c.Mode, c.Scenario), hostTimeout)
	if err != nil {
		return Report{}, 0, err
	}
	rctx, cancel := context.WithCancel(ctx)
	relayed := make(chan error, 1)
	go func() { relayed <- rl.Serve(rctx) }()
	stopRelay := func() error { cancel(); return <-relayed }

	out, err := r.Launcher.Agent(ctx, AgentConfig{Dir: c.Dir, Mode: c.Mode, Target: rl.Addr(), Scenario: c.Scenario, Calls: c.Calls, Create: create, Wait: wait})
	if err != nil {
		return Report{}, 0, errors.Join(fmt.Errorf("agent: %w", err), stopRelay())
	}
	if err := stopRelay(); err != nil {
		return Report{}, 0, err
	}
	records := rl.Records()
	if c.Scenario == s.Replay && len(records) > 0 && len(records[0].Requests) > 0 {
		answers, err := relay.Replay(ctx, addr, records[0].Requests, 3*time.Second)
		if err != nil {
			return Report{}, 0, fmt.Errorf("replay: %w", err)
		}
		rep.Replay = &ReplayOutcome{Answered: len(answers)}
	}
	if err := stopAll(); err != nil {
		return Report{}, 0, err
	}
	if rep.Ledger, err = transfer.ReadEntries(ledger); err != nil {
		return Report{}, 0, err
	}

	rep.Calls = out.Calls
	rep.Accepted = len(out.Calls) > 0
	for _, call := range out.Calls {
		rep.Accepted = rep.Accepted && call.Error == ""
	}
	rep.Frames = make([]Frames, len(records))
	rep.Changed = []FaultChange{}
	for i, rec := range records {
		rep.Frames[i] = Frames{Requests: len(rec.Requests), Responses: len(rec.Responses)}
		for _, c := range rec.Changed {
			dir := "request"
			if c.Direction == relay.Response {
				dir = "response"
			}
			rep.Changed = append(rep.Changed, FaultChange{Direction: dir, Index: c.Index})
		}
	}
	rep.Harms = assess(rep.Requested, rep.Calls, rep.Ledger)
	rep.Mismatches = mismatches(rep)
	rep.AsExpected = len(rep.Mismatches) == 0
	rep.Latency = latency(rep.Calls)
	return rep, out.WaitedMS, nil
}
