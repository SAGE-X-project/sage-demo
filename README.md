# SAGE demo

A runnable comparison of the same agent tool workload with and without SAGE
0.10.0 protection. A scripted agent asks a tool host to run one mock transfer;
a relay between them injects a fault; the demo reports what the agent saw and
what the tool host's ledger holds. No real money or external service is
involved, and faults touch only the demo's own processes on loopback.

The design, the scenario table and the residual risks are in
[`docs/design.md`](docs/design.md).

## Requirements

Go 1.26 on macOS or Linux. All processes run as the current account.

## Walkthrough

```sh
go build -o demo ./cmd/demo

# SAGE keys, operator approval and host state. Waits about six minutes once:
# new host state has a 360-second replay quarantine.
./demo setup -dir /tmp/sage-demo

# Each run starts the role processes, injects the scenario's fault and
# writes a JSON report. It exits nonzero when the result departs from the
# design.
./demo run -dir /tmp/sage-demo -mode baseline -scenario tamper-args
./demo run -dir /tmp/sage-demo -mode sage     -scenario tamper-args

# Latency over repeated normal calls.
./demo run -dir /tmp/sage-demo -mode sage -scenario normal -calls 10

./demo compare -dir /tmp/sage-demo
```

The directory path must be short (signer sockets live in it). Scenarios are
`normal`, `tamper-args`, `replay`, `unapproved` and `forge-result`. A baseline
run needs no setup.

`demo run` is the relay process. It starts the tool host and the agent, and in
SAGE mode one signer per identity, as child processes of the same binary.

## What one run on a laptop showed

Measured on 2026-10-11 (macOS, Apple silicon) with ADK
`v0.0.0-20261010085535-c7890e1ba443` and the Go core
`v1.5.3-0.20261009164347-40809355bd4d`, every role in its own process:

```
scenario      baseline                                                     sage
normal        accepted, 10 transfers, no harm                              accepted, 10 transfers, no harm
tamper-args   accepted, 1 transfers, HARMED (2)                            refused, 0 transfers, no harm
replay        accepted, resend answered 2 frames, 2 transfers, HARMED (1)  accepted, resend answered 0 frames, 1 transfers, no harm
unapproved    accepted, 1 transfers, HARMED (1)                            refused, 0 transfers, no harm
forge-result  accepted, 1 transfers, HARMED (1)                            refused, 1 transfers (ran, but the agent did not accept the result), no harm

baseline normal latency: median 3.32 ms, min 2.23 ms, max 12.11 ms over 10 calls
sage normal latency: median 1251.37 ms, min 1229.66 ms, max 1432.15 ms over 10 calls
sage setup: 366.5 s in total, of which 365.0 s waiting out the replay quarantine
```

Without protection, the relay sent 990 to another recipient, ran the transfer
twice, and made the agent accept a result the tool never returned; an agent
asking for a transfer outside the approved one got it. With SAGE the same
faults left no harm. The changed request and the forged result were refused,
the resent connection got no answer, and the unapproved call never left the
agent. In forge-result the transfer did run; the agent refused the changed
result, so it knows the call is unconfirmed rather than believing a false
balance.

The ADK caller polls for the result every 1.1 seconds, which puts a floor of
about 1.1 s under each SAGE call; the rest of the call time was not broken
down. The setup wait is a one-time cost per directory.

## Reading a report

Reports are in `DIR/reports/`. Each lists the pinned revisions, what the agent
saw for each call, the ledger, the frames the relay forwarded and the frames
its fault changed, the harms found, and `mismatches` against the design
(`as_expected` is true when there are none).

The SAGE host returns one error code for every refusal, so a report does not
say which check refused a call. It shows how far the connection got instead.

## Limits

A demo run is not a conformance verdict, a deployment security claim or a
universal claim. The Registry Source is a local file whose readiness flags are
asserted, the tool measurement accepts any snapshot, and all processes share
one account and kernel. SAGE does not stop a compromised agent host from
issuing the approved operation, a compromised tool host from acting within its
session, or a relay from dropping traffic; see the design's residual risks.

Licensed under the GNU Lesser General Public License v3.0 (see `LICENSE`).
