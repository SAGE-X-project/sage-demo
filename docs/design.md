# Demo design

This demo runs one agent tool workload twice: once over an unprotected MCP
connection and once through the SAGE 0.10.0 protected host that `sage-adk`
assembles from the Go core. The same faults are injected into both runs, and
the demo reports what each run executed and where SAGE refused. It is stage 6
of the [approved program sequence](https://github.com/SAGE-X-project/sage-spec/blob/85fee1830b2bc0d2420557df40796ae83073de12/architecture/program-sequence.md)
and builds on the [core and host integration verdict](https://github.com/SAGE-X-project/sage-inspector/blob/main/docs/core-host-integration-verdict.md).

The frozen normative source is `1820ab5eafb843e1c13f4c46c34aeeb28d934ac9`.
The demo pins ADK `c7890e1ba4438d74e2a0b4684f8575d3e5df53a9` and the Go core
module version that ADK pins.

## Goals and non-goals

The demo shows, with measurements, which of four faults change the effect of
an agent's tool call without SAGE and what SAGE does with the same fault. It
also measures the cost of protection: call latency and the one-time setup.

It does not establish conformance, deployment security or a universal claim.
It inherits every limit of the integration verdict: the Registry Source is a
local file whose readiness flags are asserted, the tool measurement accepts any
snapshot, and all processes share one kernel. The demo is not an attack tool;
faults are injected only into the demo's own processes on loopback.

## Workload

A scripted agent, with no language model, asks a tool host to run one
transfer:

```json
{"amount":10,"currency":"DEMO","to":"did:sage:web:agent.example:carol"}
```

The `transfer` tool appends the transfer to a local ledger file and returns
`{"balance":...,"transfer_id":...}`. No real money or external service is
involved. The ledger file is the effect the demo inspects after each run: a
run is harmed when the ledger holds a transfer the agent did not ask for, holds
the asked transfer twice, or when the agent accepts a result the tool did not
return.

A scripted agent keeps the two runs byte-for-byte comparable and free of
model cost. It cannot show the model-facing path (prompt injection and model
choice of tools); that path is outside this demo.

## Topology

Both runs use the same three roles, each a separate process:

- **Agent (caller)**: Alice. Issues the transfer request and checks the result.
- **Relay**: a TCP forwarder between the agent and the tool host. It stands for
  any intermediary on the path (proxy, gateway, compromised network hop). It
  can pass traffic, change bytes, or record and resend a connection.
- **Tool host (receiver)**: Bob. Runs the `transfer` tool and owns the ledger.

The baseline run speaks MCP JSON-RPC 2.0 (`initialize`, `tools/call`) over TCP
with no authentication or integrity protection, as an unprotected MCP
deployment does. It represents the message shape, not a specific MCP SDK.

The SAGE run adds, as separate processes, one `adk-signer` per identity that
holds the Ed25519 signing key and (for Bob) the X25519 KEM key, and an
operator approval made with `adk-approve`. The agent opens `guardhost.OpenCaller`
and the tool host opens `guardhost.OpenReceiver`. The `transfer` tool enters
the host through a demo `guardbinding.Factory`; the approved exact operation
is the transfer above and nothing else.

## Scenarios

| Scenario | Fault | Baseline expectation | SAGE expectation |
| --- | --- | --- | --- |
| `normal` | None. | One transfer, result accepted. | Same. |
| `tamper-args` | The relay changes the recipient and amount in the agent's request. | The tool executes the changed transfer. | The receiver refuses the changed bytes before any effect; the ledger stays empty. |
| `replay` | The relay records a completed request and resends it on a new connection. | The tool executes the transfer a second time. | The receiver's replay journal and admission ledger refuse the second attempt; one transfer only. |
| `unapproved` | The agent script asks for a transfer outside the approved operation (other recipient, larger amount). | The tool executes it. | The caller's exact-operation policy refuses before an intent is issued; nothing reaches the receiver. |
| `forge-result` | The relay changes the tool result on its way back. | The agent accepts the changed result. | The agent refuses the result; the ledger shows the real transfer. |

In the SAGE run the relay cannot read the encrypted, signed exchange. It
changes or resends bytes at the same point of the connection as in the
baseline, so the refusal shows up at the transport or signature check, not as
a policy decision about the new recipient. The report records the refusal
stage and error code exactly as the host returns them, and a scenario whose
outcome differs from the expectation is reported as such, not adjusted.

## Measurements and report

`demo run` writes one JSON report per run with: the pinned revisions, the
mode and scenario, the agent-visible outcome, the ledger entries after the
run, the refusal stage and error code (SAGE), and timings. Latency is measured
for the `normal` scenario over repeated calls in both modes. The one-time
setup cost is reported separately: a new SAGE replay journal enforces a
360-second quarantine before its first call, so `demo setup` creates state
once and later runs reopen it without waiting.

`demo compare` prints the baseline and SAGE reports side by side.

## Residual risks the demo states

The demo report lists what SAGE does not defend here, so that a refused fault
is not read as complete protection:

- An agent host that is itself compromised can still issue the approved
  operation; SAGE limits it to that exact operation.
- The tool host after decryption, and the signer's per-handshake X25519 shared
  value, are trusted; a compromised tool host can act within the session.
- An operator approval key compromise lets an attacker approve new operations.
- The relay can always drop or delay traffic; availability is not protected.
- The Registry Source, the measurement and the account separation are
  synthetic or local, as in the integration verdict.

## Layout and commands

```
cmd/demo/            setup, run, compare and the per-role subcommands
internal/transfer/   the transfer tool, its ledger and the guardbinding factory
internal/relay/      the TCP relay and its fault modes
internal/baseline/   unprotected MCP JSON-RPC agent and tool host
internal/protected/  guardhost caller and receiver assembly, synthetic Source
                     and accept-all measurement (named as such)
```

```
demo setup   -dir DIR                     # keys, approval, state; waits out the quarantine once
demo run     -dir DIR -mode baseline|sage -scenario NAME
demo compare -dir DIR
```

The default run uses the current account on macOS or Linux. A separate-account
Linux run, like the ADK qualification, is a later addition.

## Delivery order

1. This design.
2. The transfer tool, its ledger and the relay, with unit tests.
3. The baseline agent and tool host, and the baseline scenarios.
4. The SAGE agent and tool host through `guardhost`, and the SAGE scenarios.
5. Reports, `demo compare`, an end-to-end CI job and the README walkthrough.

Each step is a pull request with unit tests and, from step 3, a bounded
runtime test.
