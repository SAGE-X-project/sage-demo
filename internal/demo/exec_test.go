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

func execLauncher(t *testing.T) Exec {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Exec{Path: self, Env: []string{childEnv}, Stderr: os.Stderr}
}

// The baseline runs with each role in its own process.
func TestExecBaselineProcesses(t *testing.T) {
	dir := shortDir(t)
	runner := Runner{Launcher: execLauncher(t)}
	for _, n := range []s.Name{s.TamperArgs, s.Replay} {
		rep, err := runner.Run(context.Background(), Config{Dir: dir, Mode: Baseline, Scenario: n, Calls: 1})
		if err != nil || !rep.AsExpected || !rep.Harmed() {
			t.Fatalf("%s: %+v, %v", n, rep, err)
		}
	}
}

// The SAGE signers, tool host and agent start as separate processes with the
// system clock. Without setup's wait, new host state is inside its replay
// quarantine, so the call is refused and nothing is transferred. The full
// SAGE process run is the end-to-end CI job, which waits the real quarantine.
func TestExecSageProcessesRefuseDuringQuarantine(t *testing.T) {
	dir := shortDir(t)
	if err := protected.Setup(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(reportDir(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	runner := Runner{Launcher: execLauncher(t)}
	rep, _, err := runner.execute(context.Background(), Config{Dir: dir, Mode: Sage, Scenario: s.Normal, Calls: 1}, "quarantine", true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Accepted || rep.Harmed() || len(rep.Ledger) != 0 || rep.Calls[0].Error == "" {
		t.Fatalf("report = %+v", rep)
	}
	// The agent signed and sent its first handshake frame through the relay,
	// so its signer and the wiring work; the tool host, in quarantine, did
	// not answer it.
	if len(rep.Frames) != 1 || rep.Frames[0] != (Frames{Requests: 1}) {
		t.Fatalf("frames = %+v", rep.Frames)
	}
}

func TestExecRefusals(t *testing.T) {
	ctx := context.Background()
	if _, err := (Exec{Path: "/nonexistent/demo"}).Signer(ctx, "/tmp/x", protected.Agent); err == nil {
		t.Fatal("missing binary started")
	}
	if _, _, err := (Exec{Path: "/nonexistent/demo"}).ToolHost(ctx, ToolHostConfig{}); err == nil {
		t.Fatal("missing binary started")
	}
	if _, err := (Exec{Path: "/nonexistent/demo"}).Agent(ctx, AgentConfig{}); err == nil {
		t.Fatal("missing binary started")
	}
	e := execLauncher(t)
	e.Stderr = &bytes.Buffer{}
	// A signer without keys exits before it is ready.
	if _, err := e.Signer(ctx, shortDir(t), protected.Agent); err == nil || !strings.Contains(err.Error(), "before it was ready") {
		t.Fatalf("Signer() = %v", err)
	}
	if _, _, err := e.ToolHost(ctx, ToolHostConfig{Dir: "/tmp", Mode: "other", Ledger: "/tmp/x"}); err == nil {
		t.Fatal("tool host with an unknown mode started")
	}
	if _, err := e.Agent(ctx, AgentConfig{Dir: "/tmp", Mode: Baseline, Target: "127.0.0.1:1", Scenario: "other", Calls: 1}); err == nil {
		t.Fatal("agent with an unknown scenario ran")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := e.Signer(cancelled, shortDir(t), protected.Agent); err == nil {
		t.Fatal("signer started with a cancelled context")
	}
}

func TestProcessStopKillsAStuckProcess(t *testing.T) {
	p, err := (Exec{Path: "/bin/sh"}).start("-c", `trap "" INT; echo ready; sleep 60`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := p.Stop(); err == nil || !strings.Contains(err.Error(), "killed") {
		t.Fatalf("Stop() = %v", err)
	}
	if time.Since(start) > stopTimeout+5*time.Second {
		t.Fatal("Stop did not kill in time")
	}
	if err := p.Stop(); err == nil {
		t.Fatal("second Stop lost the exit error")
	}
}
