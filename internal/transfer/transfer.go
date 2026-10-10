// SPDX-License-Identifier: LGPL-3.0-or-later

// Package transfer is the demo's mock transfer tool. It moves no real money:
// each accepted transfer is appended to a local ledger file, which is the
// effect the demo inspects after a run.
package transfer

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sync"
)

// Tool is the tool name both demo modes call.
const Tool = "transfer"

// Currency is the only accepted currency. It names no real asset.
const Currency = "DEMO"

// MaxAmount bounds one transfer.
const MaxAmount = 1_000_000

var (
	// ErrInvalid refuses malformed transfer arguments.
	ErrInvalid = errors.New("transfer: invalid arguments")
	// ErrFunds refuses a transfer larger than the remaining balance.
	ErrFunds = errors.New("transfer: insufficient balance")
	// ErrLedger reports an unreadable or unwritable ledger file.
	ErrLedger = errors.New("transfer: ledger unavailable")

	recipient = regexp.MustCompile(`^did:sage:web:[a-z0-9.-]{1,64}:[a-z0-9-]{1,32}$`)
)

// Request is the exact argument object of one transfer.
type Request struct {
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
	To       string `json:"to"`
}

// Entry is one ledger line.
type Entry struct {
	ID       string `json:"id"`
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
	To       string `json:"to"`
}

// Result is the tool output returned to the caller.
type Result struct {
	Balance    int64  `json:"balance"`
	TransferID string `json:"transfer_id"`
}

// Parse decodes and validates transfer arguments. Every field is required,
// unknown fields and trailing data are refused.
func Parse(raw []byte) (Request, error) {
	var fields map[string]json.RawMessage
	if err := strict(raw, &fields); err != nil || len(fields) != 3 {
		return Request{}, ErrInvalid
	}
	var req Request
	if err := strict(raw, &req); err != nil {
		return Request{}, ErrInvalid
	}
	if req.Amount < 1 || req.Amount > MaxAmount || req.Currency != Currency || !recipient.MatchString(req.To) {
		return Request{}, ErrInvalid
	}
	return req, nil
}

func strict(raw []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return ErrInvalid
	}
	return nil
}

// Ledger owns one ledger file. Callers must ensure that only one process
// owns a ledger file at a time; this is not enforced.
type Ledger struct {
	mu      sync.Mutex
	path    string
	balance int64
	count   int
}

// OpenLedger opens or creates the ledger file at path. The balance is the
// opening balance minus every recorded transfer.
func OpenLedger(path string, opening int64) (*Ledger, error) {
	if opening < 0 {
		return nil, ErrInvalid
	}
	f, err := os.OpenFile(path, os.O_RDONLY|os.O_CREATE, 0o600)
	if err != nil {
		return nil, ErrLedger
	}
	defer func() { _ = f.Close() }()
	entries, err := read(f)
	if err != nil {
		return nil, err
	}
	l := &Ledger{path: path, balance: opening, count: len(entries)}
	for _, e := range entries {
		l.balance -= e.Amount
	}
	if l.balance < 0 {
		return nil, ErrLedger
	}
	return l, nil
}

// Apply validates raw arguments, records the transfer durably and returns the
// canonical result. A refused transfer records nothing; if the write itself
// fails after bytes reached the file, the ledger may need inspection.
func (l *Ledger) Apply(raw []byte) ([]byte, error) {
	req, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if req.Amount > l.balance {
		return nil, ErrFunds
	}
	entry := Entry{ID: fmt.Sprintf("t-%06d", l.count+1), Amount: req.Amount, Currency: req.Currency, To: req.To}
	line, err := json.Marshal(entry)
	if err != nil {
		return nil, ErrLedger
	}
	if err := appendLine(l.path, line); err != nil {
		return nil, err
	}
	l.count++
	l.balance -= req.Amount
	return json.Marshal(Result{Balance: l.balance, TransferID: entry.ID})
}

// Entries returns the recorded transfers in order.
func (l *Ledger) Entries() ([]Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return ReadEntries(l.path)
}

// ReadEntries reads a ledger file without owning it.
func ReadEntries(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, ErrLedger
	}
	defer func() { _ = f.Close() }()
	return read(f)
}

func read(r io.Reader) ([]Entry, error) {
	entries := []Entry{}
	s := bufio.NewScanner(r)
	for s.Scan() {
		var e Entry
		if err := strict(s.Bytes(), &e); err != nil || e.Amount < 1 {
			return nil, ErrLedger
		}
		entries = append(entries, e)
	}
	if s.Err() != nil {
		return nil, ErrLedger
	}
	return entries, nil
}

func appendLine(path string, line []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return ErrLedger
	}
	_, werr := f.Write(append(line, '\n'))
	serr := f.Sync()
	cerr := f.Close()
	if werr != nil || serr != nil || cerr != nil {
		return ErrLedger
	}
	return nil
}
