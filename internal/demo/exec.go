// SPDX-License-Identifier: LGPL-3.0-or-later
package demo

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sage-x-project/sage-demo/internal/protected"
)

const (
	readyTimeout = 30 * time.Second
	stopTimeout  = 10 * time.Second
)

// Exec starts each role as a separate process of the demo binary at Path.
// A role process prints "ready" (and its address) on its first stdout line
// once it serves, and ends cleanly on an interrupt.
type Exec struct {
	Path string
	// Env is added to the inherited environment.
	Env []string
	// Stderr receives the role processes' diagnostics. Several processes
	// write to it at once; wrap a writer that is not an *os.File in
	// SyncWriter.
	Stderr io.Writer
}

// SyncWriter serializes writes to W.
type SyncWriter struct {
	mu sync.Mutex
	W  io.Writer
}

func (s *SyncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.W.Write(p)
}

type process struct {
	cmd    *exec.Cmd
	stdout *os.File
	done   chan error
}

func (e Exec) start(args ...string) (*process, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(e.Path, args...)
	cmd.Env = append(os.Environ(), e.Env...)
	cmd.Stdout = w
	cmd.Stderr = e.Stderr
	err = cmd.Start()
	_ = w.Close()
	if err != nil {
		_ = r.Close()
		return nil, err
	}
	p := &process{cmd: cmd, stdout: r, done: make(chan error, 1)}
	go func() { p.done <- cmd.Wait() }()
	return p, nil
}

// ready reads the first stdout line and returns what follows "ready".
func (p *process) ready(ctx context.Context) (string, error) {
	line := make(chan string, 1)
	go func() {
		s := bufio.NewScanner(p.stdout)
		if s.Scan() {
			line <- s.Text()
		}
		close(line)
		_, _ = io.Copy(io.Discard, p.stdout)
	}()
	t := time.NewTimer(readyTimeout)
	defer t.Stop()
	select {
	case l, ok := <-line:
		if rest, found := strings.CutPrefix(l, "ready"); ok && found {
			return strings.TrimSpace(rest), nil
		}
		return "", errors.Join(errors.New("role process exited before it was ready"), p.Stop())
	case <-t.C:
		return "", errors.Join(errors.New("role process did not become ready"), p.Stop())
	case <-ctx.Done():
		return "", errors.Join(ctx.Err(), p.Stop())
	}
}

// Stop interrupts the process and waits; it kills a process that does not
// exit within the stop timeout.
func (p *process) Stop() error {
	defer func() { _ = p.stdout.Close() }()
	_ = p.cmd.Process.Signal(os.Interrupt)
	t := time.NewTimer(stopTimeout)
	defer t.Stop()
	select {
	case err := <-p.done:
		p.done <- err
		return err
	case <-t.C:
		_ = p.cmd.Process.Kill()
		err := <-p.done
		p.done <- err
		return errors.Join(errors.New("role process did not stop; killed"), err)
	}
}

// Signer starts `demo signer`.
func (e Exec) Signer(ctx context.Context, dir string, role protected.Role) (Process, error) {
	p, err := e.start("signer", "-dir", dir, "-role", string(role))
	if err != nil {
		return nil, err
	}
	if _, err := p.ready(ctx); err != nil {
		return nil, err
	}
	return p, nil
}

// ToolHost starts `demo toolhost`.
func (e Exec) ToolHost(ctx context.Context, c ToolHostConfig) (Process, string, error) {
	p, err := e.start("toolhost", "-dir", c.Dir, "-mode", string(c.Mode), "-ledger", c.Ledger, "-create="+strconv.FormatBool(c.Create))
	if err != nil {
		return nil, "", err
	}
	addr, err := p.ready(ctx)
	if err != nil {
		return nil, "", err
	}
	if addr == "" {
		return nil, "", errors.Join(errors.New("tool host reported no address"), p.Stop())
	}
	return p, addr, nil
}

// Agent runs `demo agent` and decodes its outcome from stdout.
func (e Exec) Agent(ctx context.Context, c AgentConfig) (Outcome, error) {
	p, err := e.start("agent", "-dir", c.Dir, "-mode", string(c.Mode), "-target", c.Target, "-scenario", string(c.Scenario),
		"-calls", strconv.Itoa(c.Calls), "-create="+strconv.FormatBool(c.Create), "-wait", c.Wait.String())
	if err != nil {
		return Outcome{}, err
	}
	// On cancellation, interrupt the agent and kill it if it does not exit.
	// Closing stdout also ends the read if a descendant still holds the pipe.
	stop := context.AfterFunc(ctx, func() {
		_ = p.cmd.Process.Signal(os.Interrupt)
		t := time.AfterFunc(stopTimeout, func() { _ = p.cmd.Process.Kill(); _ = p.stdout.Close() })
		go func() { err := <-p.done; t.Stop(); p.done <- err }()
	})
	defer stop()
	raw, rerr := io.ReadAll(p.stdout)
	_ = p.stdout.Close()
	werr := <-p.done
	p.done <- werr
	if werr != nil || rerr != nil {
		return Outcome{}, errors.Join(errors.New("agent process failed"), werr, rerr)
	}
	var out Outcome
	if err := json.Unmarshal(raw, &out); err != nil {
		return Outcome{}, fmt.Errorf("agent outcome: %w", err)
	}
	return out, nil
}
