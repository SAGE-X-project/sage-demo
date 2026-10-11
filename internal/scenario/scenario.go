// SPDX-License-Identifier: LGPL-3.0-or-later

// Package scenario names the demo scenarios and the fixed values both modes
// use, so that the baseline and SAGE runs inject the same faults.
package scenario

import "fmt"

// Name identifies one scenario.
type Name string

const (
	// Normal runs the approved transfer with no fault.
	Normal Name = "normal"
	// TamperArgs changes the request arguments in the relay.
	TamperArgs Name = "tamper-args"
	// Replay resends a completed request on a new connection.
	Replay Name = "replay"
	// Unapproved makes the agent ask for a transfer outside the approved one.
	Unapproved Name = "unapproved"
	// ForgeResult changes the tool result in the relay.
	ForgeResult Name = "forge-result"
)

// All lists the scenarios in report order.
var All = []Name{Normal, TamperArgs, Replay, Unapproved, ForgeResult}

// Parse returns the scenario with the given name.
func Parse(s string) (Name, error) {
	for _, n := range All {
		if string(n) == s {
			return n, nil
		}
	}
	return "", fmt.Errorf("unknown scenario %q", s)
}

const (
	// Agent is the calling agent's DID.
	Agent = "did:sage:web:agent.example:alice"
	// ToolHost is the tool host's DID.
	ToolHost = "did:sage:web:agent.example:bob"
	// Recipient is the transfer recipient the agent intends.
	Recipient = "did:sage:web:agent.example:carol"
	// Attacker is the recipient a fault substitutes.
	Attacker = "did:sage:web:agent.example:mallory"

	// OpeningBalance is the tool host's balance before a run.
	OpeningBalance = 1000
)

var (
	// Approved is the transfer the agent intends and the operator approves,
	// in canonical form.
	Approved = []byte(`{"amount":10,"currency":"DEMO","to":"did:sage:web:agent.example:carol"}`)
	// Substituted is what the relay writes in place of Approved and what the
	// agent asks for in the unapproved scenario.
	Substituted = []byte(`{"amount":990,"currency":"DEMO","to":"did:sage:web:agent.example:mallory"}`)
	// ForgedResult is what the relay writes in place of the tool result. It
	// differs from the real result of the approved transfer
	// ({"balance":990,"transfer_id":"t-000001"}): it hides the debit and names
	// a transfer the tool never made.
	ForgedResult = []byte(`{"balance":1000,"transfer_id":"t-000999"}`)
)
