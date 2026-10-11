// SPDX-License-Identifier: LGPL-3.0-or-later
package demo

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/sage-x-project/sage-adk/core/guardservices"

	"github.com/sage-x-project/sage-demo/internal/protected"
	s "github.com/sage-x-project/sage-demo/internal/scenario"
)

const usage = `usage:
  demo setup   -dir DIR                    create SAGE keys, approval and state; waits about six minutes once
  demo run     -dir DIR -mode baseline|sage -scenario NAME [-calls N]
  demo compare -dir DIR

scenarios: normal, tamper-args, replay, unapproved, forge-result

setup and run start these role processes themselves:
  demo signer   -dir DIR -role alice|bob
  demo toolhost -dir DIR -mode MODE -ledger FILE [-create]
  demo agent    -dir DIR -mode MODE -target ADDR -scenario NAME [-calls N] [-create] [-wait D]
`

// errUsage marks a command line that does not parse.
var errUsage = errors.New("invalid command line")

// Main runs the demo command line and returns the process exit code.
func Main(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if err := command(ctx, args, stdout, stderr); err != nil {
		if errors.Is(err, errUsage) {
			_, _ = fmt.Fprint(stderr, usage)
			return 2
		}
		_, _ = fmt.Fprintln(stderr, "demo:", err)
		return 1
	}
	return 0
}

func command(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errUsage
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dir := fs.String("dir", "", "demo directory")
	mode := fs.String("mode", "", "baseline or sage")
	name := fs.String("scenario", "", "scenario name")
	calls := fs.Int("calls", 1, "agent calls")
	role := fs.String("role", "", "signer role")
	ledger := fs.String("ledger", "", "ledger file")
	target := fs.String("target", "", "address the agent dials")
	create := fs.Bool("create", false, "open new host state")
	wait := fs.Duration("wait", 0, "delay before the first call")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 || *dir == "" {
		return errUsage
	}
	switch args[0] {
	case "setup", "run":
		self, err := os.Executable()
		if err != nil {
			return err
		}
		r := Runner{Launcher: Exec{Path: self, Stderr: &SyncWriter{W: stderr}}}
		var rep Report
		if args[0] == "setup" {
			if _, err := fmt.Fprintf(stdout, "setting up %s; waiting %s for the replay quarantine\n", *dir, QuarantineWait); err != nil {
				return err
			}
			rep, err = r.Setup(ctx, *dir)
		} else {
			m, merr := ParseMode(*mode)
			n, nerr := s.Parse(*name)
			if merr != nil || nerr != nil {
				return errors.Join(errUsage, merr, nerr)
			}
			rep, err = r.Run(ctx, Config{Dir: *dir, Mode: m, Scenario: n, Calls: *calls})
		}
		if rep.Name != "" {
			err = errors.Join(err, summarize(stdout, *dir, rep))
			if err == nil && !rep.AsExpected {
				err = errors.New("the run differs from the design")
			}
		}
		return err
	case "compare":
		reports, err := ReadReports(*dir)
		if err != nil {
			return err
		}
		return Compare(stdout, reports)
	case "signer":
		return ServeSigner(ctx, *dir, protected.Role(*role), func() { _, _ = fmt.Fprintln(stdout, "ready") })
	case "toolhost":
		m, err := ParseMode(*mode)
		if err != nil || *ledger == "" {
			return errors.Join(errUsage, err)
		}
		return ServeToolHost(ctx, ToolHostConfig{Dir: *dir, Mode: m, Ledger: *ledger, Create: *create}, guardservices.NewSystemClock(),
			func(addr string) { _, _ = fmt.Fprintln(stdout, "ready", addr) })
	case "agent":
		m, merr := ParseMode(*mode)
		n, nerr := s.Parse(*name)
		if merr != nil || nerr != nil || *target == "" || *calls < 1 || *calls > MaxCalls {
			return errors.Join(errUsage, merr, nerr)
		}
		out, err := RunAgent(ctx, AgentConfig{Dir: *dir, Mode: m, Target: *target, Scenario: n, Calls: *calls, Create: *create, Wait: *wait},
			guardservices.NewSystemClock(), Sleep)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(out)
	}
	return errUsage
}

// printer keeps the first write error, so a report prints without checking
// every line.
type printer struct {
	w   io.Writer
	err error
}

func (p *printer) f(format string, args ...any) {
	if p.err == nil {
		_, p.err = fmt.Fprintf(p.w, format, args...)
	}
}

func summarize(w io.Writer, dir string, r Report) error {
	p := &printer{w: w}
	outcome := "refused"
	if r.Accepted {
		outcome = "accepted"
	}
	p.f("%s %s: %s, %d transfers in the ledger, %d harms", r.Mode, r.Scenario, outcome, len(r.Ledger), len(r.Harms))
	if !r.AsExpected {
		p.f(", DIFFERS FROM EXPECTATION")
	}
	p.f("\n")
	for _, m := range r.Mismatches {
		p.f("  mismatch: %s\n", m)
	}
	for _, h := range r.Harms {
		p.f("  harm: %s\n", h)
	}
	for i, c := range r.Calls {
		if c.Error != "" {
			p.f("  call %d: %s after %s\n", i+1, c.Error, time.Duration(c.ElapsedMS*float64(time.Millisecond)).Round(time.Millisecond))
		}
	}
	p.f("report: %s\n", ReportPath(dir, r.Name))
	return p.err
}
