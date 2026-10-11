// SPDX-License-Identifier: LGPL-3.0-or-later
package scenario

import (
	"testing"

	"github.com/sage-x-project/sage-demo/internal/transfer"
)

func TestParse(t *testing.T) {
	for _, n := range All {
		if got, err := Parse(string(n)); err != nil || got != n {
			t.Fatalf("Parse(%q) = %q, %v", n, got, err)
		}
	}
	if _, err := Parse("other"); err == nil {
		t.Fatal("Parse accepted an unknown scenario")
	}
}

func TestFixedValuesAreValidTransfers(t *testing.T) {
	approved, err := transfer.Parse(Approved)
	if err != nil || approved.To != Recipient {
		t.Fatalf("Approved = %+v, %v", approved, err)
	}
	substituted, err := transfer.Parse(Substituted)
	if err != nil || substituted.To != Attacker || substituted.Amount > OpeningBalance {
		t.Fatalf("Substituted = %+v, %v", substituted, err)
	}
}

// The forged result must differ from what the tool returns for the approved
// transfer, or the forge-result fault would change nothing.
func TestForgedResultDiffersFromTheRealResult(t *testing.T) {
	ledger, err := transfer.OpenLedger(t.TempDir()+"/ledger.jsonl", OpeningBalance)
	if err != nil {
		t.Fatal(err)
	}
	real, err := ledger.Apply(Approved)
	if err != nil || string(real) == string(ForgedResult) {
		t.Fatalf("real result %s, forged %s, %v", real, ForgedResult, err)
	}
}
