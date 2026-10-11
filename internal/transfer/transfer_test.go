// SPDX-License-Identifier: LGPL-3.0-or-later
package transfer

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

const carol = "did:sage:web:agent.example:carol"

func TestParse(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		ok   bool
	}{
		{"valid", `{"amount":10,"currency":"DEMO","to":"did:sage:web:agent.example:carol"}`, true},
		{"field order is free", `{"to":"did:sage:web:agent.example:carol","amount":10,"currency":"DEMO"}`, true},
		{"missing field", `{"amount":10,"to":"did:sage:web:agent.example:carol"}`, false},
		{"unknown field", `{"amount":10,"currency":"DEMO","to":"did:sage:web:agent.example:carol","memo":"x"}`, false},
		{"trailing data", `{"amount":10,"currency":"DEMO","to":"did:sage:web:agent.example:carol"} {}`, false},
		{"zero amount", `{"amount":0,"currency":"DEMO","to":"did:sage:web:agent.example:carol"}`, false},
		{"amount above limit", `{"amount":1000001,"currency":"DEMO","to":"did:sage:web:agent.example:carol"}`, false},
		{"fractional amount", `{"amount":1.5,"currency":"DEMO","to":"did:sage:web:agent.example:carol"}`, false},
		{"other currency", `{"amount":10,"currency":"USD","to":"did:sage:web:agent.example:carol"}`, false},
		{"not a sage did", `{"amount":10,"currency":"DEMO","to":"carol"}`, false},
		{"not json", `amount=10`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := Parse([]byte(tt.raw))
			if tt.ok != (err == nil) {
				t.Fatalf("Parse() error = %v, want ok %v", err, tt.ok)
			}
			if tt.ok && req != (Request{Amount: 10, Currency: Currency, To: carol}) {
				t.Fatalf("Parse() = %+v", req)
			}
		})
	}
}

func TestLedgerRecordsTransfersAndReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.jsonl")
	l, err := OpenLedger(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	out, err := l.Apply([]byte(`{"amount":10,"currency":"DEMO","to":"did:sage:web:agent.example:carol"}`))
	if err != nil || string(out) != `{"balance":90,"transfer_id":"t-000001"}` {
		t.Fatalf("Apply() = %s, %v", out, err)
	}
	if _, err := l.Apply([]byte(`{"amount":91,"currency":"DEMO","to":"did:sage:web:agent.example:carol"}`)); !errors.Is(err, ErrFunds) {
		t.Fatalf("overdraft error = %v, want ErrFunds", err)
	}
	if _, err := l.Apply([]byte(`{"amount":-1}`)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid error = %v, want ErrInvalid", err)
	}
	want := []Entry{{ID: "t-000001", Amount: 10, Currency: Currency, To: carol}}
	if got, err := l.Entries(); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Entries() = %+v, %v", got, err)
	}

	reopened, err := OpenLedger(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	out, err = reopened.Apply([]byte(`{"amount":90,"currency":"DEMO","to":"did:sage:web:agent.example:carol"}`))
	if err != nil || string(out) != `{"balance":0,"transfer_id":"t-000002"}` {
		t.Fatalf("Apply() after reopen = %s, %v", out, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("ledger mode = %v, %v", info.Mode(), err)
	}
}

func TestOpenLedgerRefusesInvalidState(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	tests := []struct {
		name    string
		path    string
		opening int64
	}{
		{"negative opening balance", filepath.Join(dir, "new.jsonl"), -1},
		{"corrupt line", write("corrupt.jsonl", "{not json}\n"), 100},
		{"non-positive amount", write("zero.jsonl", `{"id":"t-000001","amount":0,"currency":"DEMO","to":"x"}`+"\n"), 100},
		{"overspent", write("over.jsonl", `{"id":"t-000001","amount":101,"currency":"DEMO","to":"x"}`+"\n"), 100},
		{"missing directory", filepath.Join(dir, "absent", "ledger.jsonl"), 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := OpenLedger(tt.path, tt.opening); err == nil {
				t.Fatal("OpenLedger() succeeded")
			}
		})
	}
}

func TestLedgerWriteFailureRecordsNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.jsonl")
	l, err := OpenLedger(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Apply([]byte(`{"amount":10,"currency":"DEMO","to":"did:sage:web:agent.example:carol"}`)); !errors.Is(err, ErrLedger) {
		t.Fatalf("Apply() error = %v, want ErrLedger", err)
	}
	if _, err := l.Entries(); !errors.Is(err, ErrLedger) {
		t.Fatalf("Entries() error = %v, want ErrLedger", err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := l.Apply([]byte(`{"amount":10,"currency":"DEMO","to":"did:sage:web:agent.example:carol"}`))
	if err != nil || string(out) != `{"balance":90,"transfer_id":"t-000001"}` {
		t.Fatalf("Apply() after recovery = %s, %v", out, err)
	}
}
