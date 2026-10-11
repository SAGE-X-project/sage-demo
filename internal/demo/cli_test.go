// SPDX-License-Identifier: LGPL-3.0-or-later
package demo

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	s "github.com/sage-x-project/sage-demo/internal/scenario"
	"github.com/sage-x-project/sage-demo/internal/transfer"
)

func run(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestMainUsage(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"other", "-dir", "/tmp/x"},
		{"run"},
		{"run", "-dir", "/tmp/x", "extra"},
		{"run", "-dir", "/tmp/x", "-mode", "other", "-scenario", "normal"},
		{"run", "-dir", "/tmp/x", "-mode", "baseline", "-scenario", "other"},
		{"toolhost", "-dir", "/tmp/x", "-mode", "baseline"},
		{"agent", "-dir", "/tmp/x", "-mode", "baseline", "-scenario", "normal"},
		{"run", "-unknown"},
	} {
		if code, _, stderr := run(args...); code != 2 || !strings.Contains(stderr, "usage:") {
			t.Errorf("Main(%q) = %d, %q", args, code, stderr)
		}
	}
}

// The command line runs the baseline end to end with child processes of
// this test binary, then compares the reports.
func TestMainRunAndCompare(t *testing.T) {
	dir := shortDir(t)
	t.Setenv("SAGE_DEMO_TEST_CHILD", "1")
	code, stdout, stderr := run("run", "-dir", dir, "-mode", "baseline", "-scenario", "forge-result")
	if code != 0 || !strings.Contains(stdout, "baseline forge-result: accepted") || !strings.Contains(stdout, "which matches no ledger state") {
		t.Fatalf("run = %d, %q, %q", code, stdout, stderr)
	}
	if code, stdout, _ = run("compare", "-dir", dir); code != 0 || !strings.Contains(stdout, "forge-result  accepted, 1 transfers, HARMED (1)") {
		t.Fatalf("compare = %d, %q", code, stdout)
	}
	if code, _, stderr = run("run", "-dir", dir, "-mode", "sage", "-scenario", "normal"); code != 1 || !strings.Contains(stderr, "run demo setup first") {
		t.Fatalf("sage run without setup = %d, %q", code, stderr)
	}
	if code, _, stderr = run("compare", "-dir", "/nonexistent"); code != 0 {
		t.Fatalf("compare of an empty directory = %d, %q", code, stderr)
	}
}

func TestMainSetupRefusesExistingDirectory(t *testing.T) {
	dir := shortDir(t)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if code, stdout, stderr := run("setup", "-dir", dir); code != 1 || !strings.Contains(stderr, "already exists") || !strings.Contains(stdout, "setting up") {
		t.Fatalf("setup = %d, %q, %q", code, stdout, stderr)
	}
}

func TestMainAgentPrintsOutcome(t *testing.T) {
	code, stdout, _ := run("agent", "-dir", "/tmp/x", "-mode", "baseline", "-target", "127.0.0.1:1", "-scenario", "normal")
	var out Outcome
	if code != 0 || json.Unmarshal([]byte(stdout), &out) != nil || len(out.Calls) != 1 || out.Calls[0].Error == "" {
		t.Fatalf("agent = %d, %q", code, stdout)
	}
}

func TestSummarize(t *testing.T) {
	var b bytes.Buffer
	rep := Report{Name: "n", Mode: Sage, Scenario: s.TamperArgs, Harms: []string{"h"},
		Calls: []CallOutcome{{Error: "GUARD_HOST_DENIED", ElapsedMS: 1500}}}
	if err := summarize(&b, "/d", rep); err != nil {
		t.Fatal(err)
	}
	if summarize(failing{}, "/d", rep) == nil || Compare(failing{}, []Report{rep}) == nil {
		t.Error("a failed write was not reported")
	}
	for _, want := range []string{"sage tamper-args: refused", "DIFFERS FROM EXPECTATION", "harm: h", "call 1: GUARD_HOST_DENIED after 1.5s", "report: /d/reports/n.json"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("summary lacks %q:\n%s", want, b.String())
		}
	}
}

func TestParseModeAndExpectations(t *testing.T) {
	if _, err := ParseMode("other"); err == nil {
		t.Fatal("unknown mode parsed")
	}
	if string(Requested(s.Unapproved)) != string(s.Substituted) || string(Requested(s.Normal)) != string(s.Approved) {
		t.Fatal("requested transfers")
	}
	if (Expected(Baseline, s.Normal) != Expectation{Accepted: true}) || (Expected(Sage, s.ForgeResult) != Expectation{}) {
		t.Fatal("expectations")
	}
	if Fault(Sage, s.Normal).Frame(0, 0, 0, []byte("x"))[0] != 'x' {
		t.Fatal("normal fault changed a frame")
	}
}

func TestAssess(t *testing.T) {
	approved := transfer.Entry{ID: "t-000001", Amount: 10, Currency: transfer.Currency, To: s.Recipient}
	ok := CallOutcome{Result: json.RawMessage(`{"balance":990,"transfer_id":"t-000001"}`)}
	for name, c := range map[string]struct {
		requested []byte
		calls     []CallOutcome
		ledger    []transfer.Entry
		harms     int
	}{
		"normal":           {s.Approved, []CallOutcome{ok}, []transfer.Entry{approved}, 0},
		"refused":          {s.Approved, []CallOutcome{{Error: "denied"}}, nil, 0},
		"unknown result":   {s.Approved, []CallOutcome{{Result: json.RawMessage(`{"balance":990,"transfer_id":"t-000001","x":1}`)}}, []transfer.Entry{approved}, 1},
		"forged result":    {s.Approved, []CallOutcome{{Result: json.RawMessage(s.ForgedResult)}}, []transfer.Entry{approved}, 1},
		"double execution": {s.Approved, []CallOutcome{ok}, []transfer.Entry{approved, {ID: "t-000002", Amount: 10, Currency: transfer.Currency, To: s.Recipient}}, 1},
		"unapproved asked": {s.Substituted, []CallOutcome{{Error: "denied"}}, []transfer.Entry{{ID: "t-000001", Amount: 990, Currency: transfer.Currency, To: s.Attacker}}, 1},
	} {
		if got := assess(c.requested, c.calls, c.ledger); len(got) != c.harms {
			t.Errorf("%s: harms %q, want %d", name, got, c.harms)
		}
	}
	if (latency(nil) != Latency{}) {
		t.Fatal("latency of no calls")
	}
}

func TestSleep(t *testing.T) {
	if err := Sleep(context.Background(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Sleep(ctx, time.Hour); err == nil {
		t.Fatal("Sleep ignored a cancelled context")
	}
}

func TestRoleRefusals(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{started: time.Now()}
	dir := shortDir(t)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ledger := dir + "/ledger.jsonl"
	never := func(string) { t.Error("a refused tool host reported ready") }
	for name, c := range map[string]ToolHostConfig{
		"unwritable ledger": {Dir: dir, Mode: Baseline, Ledger: "/nonexistent/ledger.jsonl"},
		"sage without keys": {Dir: dir, Mode: Sage, Ledger: ledger},
		"unknown mode":      {Dir: dir, Mode: "other", Ledger: ledger},
	} {
		if err := ServeToolHost(ctx, c, clock, never); err == nil {
			t.Errorf("%s: ServeToolHost succeeded", name)
		}
	}
	for name, c := range map[string]AgentConfig{
		"sage without keys": {Dir: dir, Mode: Sage, Scenario: s.Normal, Calls: 1},
		"unknown mode":      {Dir: dir, Mode: "other", Scenario: s.Normal, Calls: 1},
	} {
		if _, err := RunAgent(ctx, c, clock, Sleep); err == nil {
			t.Errorf("%s: RunAgent succeeded", name)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := RunAgent(cancelled, AgentConfig{Mode: Baseline, Scenario: s.Normal, Calls: 1, Wait: time.Hour}, clock, Sleep); err == nil {
		t.Error("RunAgent ignored a cancelled wait")
	}
	if err := os.MkdirAll(reportDir(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ReportPath(dir, "bad"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadReports(dir); err == nil {
		t.Error("ReadReports accepted a malformed report")
	}
	if code, _, _ := run("compare", "-dir", dir); code != 1 {
		t.Errorf("compare of a malformed report = %d", code)
	}
}

type failing struct{}

func (failing) Write([]byte) (int, error) { return 0, os.ErrClosed }

func TestMismatches(t *testing.T) {
	ok := func(m Mode, n s.Name) Report {
		e := Expected(m, n)
		return Report{Mode: m, Scenario: n, Expected: e, Accepted: e.Accepted}
	}
	for name, c := range map[string]struct {
		r    Report
		want string
	}{
		"accepted":         {Report{Mode: Sage, Scenario: s.TamperArgs, Accepted: true}, "accepted every call"},
		"harmed":           {Report{Mode: Sage, Scenario: s.Normal, Accepted: true, Expected: Expectation{Accepted: true}, Harms: []string{"h"}}, "harmed: true"},
		"tamper unchanged": {ok(Sage, s.TamperArgs), "changed no request frame"},
		"tamper too early": {ok(Sage, s.TamperArgs), "fewer than precede"},
		"forge unchanged":  {ok(Sage, s.ForgeResult), "changed no response frame"},
		"forge never ran":  {ok(Sage, s.ForgeResult), "should have run once"},
		"no resend":        {ok(Sage, s.Replay), "got no resend"},
		"resend answered":  {func() Report { r := ok(Sage, s.Replay); r.Replay = &ReplayOutcome{Answered: 1}; return r }(), "got 1 answered frames"},
		"unapproved sent":  {func() Report { r := ok(Sage, s.Unapproved); r.Frames = []Frames{{Requests: 1}}; return r }(), "reached the relay"},
	} {
		if got := strings.Join(mismatches(c.r), "; "); !strings.Contains(got, c.want) {
			t.Errorf("%s: mismatches %q lack %q", name, got, c.want)
		}
	}
	good := ok(Sage, s.TamperArgs)
	good.Changed = []FaultChange{{Direction: "request", Index: 4}}
	good.Frames = []Frames{{Requests: 5, Responses: 4}}
	if got := mismatches(good); len(got) != 0 {
		t.Errorf("a matching run has mismatches %q", got)
	}
}

func TestLockRefusesASecondCommand(t *testing.T) {
	dir := t.TempDir()
	unlock, err := lock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lock(dir); err == nil || !strings.Contains(err.Error(), "another demo command") {
		t.Fatalf("second lock = %v", err)
	}
	unlock()
	again, err := lock(dir)
	if err != nil {
		t.Fatal(err)
	}
	again()
	if _, err := lock("/nonexistent"); err == nil {
		t.Fatal("lock in a missing directory succeeded")
	}
}

// A cancelled agent that ignores the interrupt is killed after the stop
// timeout, even when a child of it still holds the stdout pipe.
func TestExecAgentIsKilledAfterCancel(t *testing.T) {
	dir := shortDir(t)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	script, trapped := dir+"/agent", dir+"/trapped"
	// The trailing command keeps the shell from replacing itself with sleep,
	// so sleep is a child that outlives the killed shell.
	body := "#!/bin/sh\ntrap '' INT\ntouch " + trapped + "\nsleep 60\ntrue\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for range 500 {
			if _, err := os.Stat(trapped); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
	}()
	start := time.Now()
	_, err := (Exec{Path: script}).Agent(ctx, AgentConfig{})
	if err == nil || !strings.Contains(err.Error(), "killed") {
		t.Fatalf("Agent() = %v", err)
	}
	if elapsed := time.Since(start); elapsed < stopTimeout || elapsed > stopTimeout+5*time.Second {
		t.Fatalf("the agent was killed after %s", elapsed)
	}
}
