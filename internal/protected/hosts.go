// SPDX-License-Identifier: LGPL-3.0-or-later
package protected

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"time"

	b "github.com/sage-x-project/sage-adk/core/guardbinding"
	"github.com/sage-x-project/sage-adk/core/guardhost"
	"github.com/sage-x-project/sage-adk/core/guardsigner"
	g "github.com/sage-x-project/sage/pkg/agent/guard010"
	r "github.com/sage-x-project/sage/pkg/agent/registry010"

	s "github.com/sage-x-project/sage-demo/internal/scenario"
	"github.com/sage-x-project/sage-demo/internal/transfer"
)

// Frame indexes of one protected call, observed with the pinned core: the
// request frame that carries the tools/call with the signed intent, and the
// first response frame that carries the signed result.
const (
	RequestCall    = 4
	ResponseResult = 4
)

// Options selects one demo directory and its shared host settings.
type Options struct {
	Dir string
	// Create is true only when the host state is new; reopening existing
	// state never recreates it.
	Create bool
	// Clock is the host clock; guardservices.SystemClock in a real run.
	Clock guardhost.Clock
	// SignerUID is the account the signer processes run as.
	SignerUID uint32
	// Timeout bounds signer requests and connection setup.
	Timeout time.Duration
}

func (o Options) valid() bool {
	return o.Dir != "" && o.Clock != nil && o.Timeout > 0
}

// ServeSigner serves role's keys on its socket until ctx ends. Only peers
// running as uid may use it. Alice's signer signs intents and handshakes;
// Bob's signs results and handshakes and holds his X25519 KEM key.
func ServeSigner(ctx context.Context, o Options, role Role, uid uint32, ready func()) error {
	l := Layout{Dir: o.Dir}
	key, err := guardsigner.LoadKeyFile(l.Key(string(role) + ".key"))
	if err != nil {
		return err
	}
	// NewServer copies Key and KEM; the loaded bytes are cleared on every return.
	defer clear(key)
	c := guardsigner.Config{Key: key, AllowedUIDs: []uint32{uid}, Timeout: o.Timeout, MaxConnections: 8}
	switch role {
	case Agent:
		c.Roles = []string{"intent", "transport"}
	case ToolHost:
		if c.KEM, err = guardsigner.LoadKEMFile(l.Key("bob.kem")); err != nil {
			return err
		}
		defer clear(c.KEM)
		c.Roles = []string{"result", "transport", "kem"}
	default:
		return errors.New("unknown signer role")
	}
	server, err := guardsigner.NewServer(c)
	if err != nil {
		return err
	}
	defer server.Close()
	if info, err := os.Lstat(l.Socket(role)); err == nil && info.Mode()&os.ModeSocket != 0 {
		_ = os.Remove(l.Socket(role))
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: l.Socket(role), Net: "unix"})
	if err != nil {
		return err
	}
	if ready != nil {
		ready()
	}
	err = server.Serve(ctx, listener)
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// common reads the shared approval material and builds the parts both hosts
// use.
type common struct {
	env      guardhost.Environment
	approved guardhost.Approved
	signer   *guardsigner.Client
	factory  *Factory
}

func assemble(o Options, role Role, ledger *transfer.Ledger) (common, error) {
	if !o.valid() {
		return common{}, guardhost.ErrDenied
	}
	l := Layout{Dir: o.Dir}
	read := func(name string) ([]byte, error) { return os.ReadFile(l.Shared(name)) }
	policy, err := read("policy.json")
	if err != nil {
		return common{}, err
	}
	manifest, err := read("manifest.json")
	if err != nil {
		return common{}, err
	}
	approval, err := read("approval.json")
	if err != nil {
		return common{}, err
	}
	pub, err := read("operator.pub")
	if err != nil {
		return common{}, err
	}
	operator, err := hex.DecodeString(string(pub))
	if err != nil || len(operator) != ed25519.PublicKeySize {
		return common{}, errors.New("invalid operator public key")
	}
	signer, err := guardsigner.NewClient(l.Socket(role), o.SignerUID, o.Timeout)
	if err != nil {
		return common{}, err
	}
	factory, err := NewFactory(ledger, AcceptAll{})
	if err != nil {
		return common{}, err
	}
	return common{
		env: guardhost.Environment{Dir: l.State(role), Create: o.Create, Registry: r.Config{Source: SourceName, Registry: Registry, Network: Network},
			Source: fileSource{path: l.Shared("registry.json"), clock: o.Clock}, Clock: o.Clock},
		approved: guardhost.Approved{
			Operation: b.Config{Directory: l.Artifacts(role), Policy: policy, Manifest: manifest, Limits: b.Limits{FileBytes: 4096, TotalBytes: 16384}, Factory: factory},
			Approval:  approval, Approvers: []ed25519.PublicKey{operator}},
		signer:  signer,
		factory: factory,
	}, nil
}

func connection(o Options, role g.MCPRole, name string) g.MCPConnectionConfig {
	c := g.MCPConnectionConfig{Role: role, Name: name, Version: "1", TTLSeconds: 300, Timeout: o.Timeout}
	if role == g.MCPInitiator {
		c.Recipient, c.RecipientKey = s.ToolHost, s.ToolHost+"#signing-1"
	}
	return c
}

// AgentHost is Alice's protected caller.
type AgentHost struct {
	host    *guardhost.Caller
	factory *Factory
}

// OpenAgent opens Alice's caller host. A new host stays in its 360-second
// replay quarantine; calls made earlier are refused.
func OpenAgent(ctx context.Context, o Options) (*AgentHost, error) {
	c, err := assemble(o, Agent, nil)
	if err != nil {
		return nil, err
	}
	host, err := guardhost.OpenCaller(ctx, guardhost.CallerConfig{
		Environment: c.env,
		Identity:    guardhost.Identity{DID: s.Agent, KeyID: s.Agent + "#signing-1", Transport: c.signer},
		Intent:      c.signer, Recipient: s.ToolHost, RecipientKey: s.ToolHost + "#signing-1",
		Approved:   c.approved,
		Bounds:     g.MCPClientHostBounds{Clients: 2, Owners: 4, Client: 20 * time.Second, Tick: time.Millisecond},
		Connection: connection(o, g.MCPInitiator, "sage demo agent"),
	})
	if err != nil {
		c.factory.Close()
		return nil, err
	}
	return &AgentHost{host: host, factory: c.factory}, nil
}

// Call asks the tool host, over conn, for the transfer with arguments and
// returns the verified result. The host refuses arguments outside the
// approved operation before issuing an intent.
func (a *AgentHost) Call(ctx context.Context, conn net.Conn, arguments []byte) ([]byte, error) {
	return a.host.Call(ctx, [][]byte{[]byte("please send the approved transfer")}, g.IntentProposal{Tool: transfer.Tool, Arguments: arguments, LifetimeSeconds: 300}, conn)
}

// Close closes the caller host.
func (a *AgentHost) Close() error {
	err := a.host.Close()
	a.factory.Close()
	return err
}

// ToolHostServer is Bob's protected receiver.
type ToolHostServer struct {
	host    *guardhost.Receiver
	factory *Factory
}

// OpenToolHost opens Bob's receiver host, which applies admitted transfers
// to ledger.
func OpenToolHost(ctx context.Context, o Options, ledger *transfer.Ledger) (*ToolHostServer, error) {
	if ledger == nil {
		return nil, guardhost.ErrDenied
	}
	c, err := assemble(o, ToolHost, ledger)
	if err != nil {
		return nil, err
	}
	host, err := guardhost.OpenReceiver(ctx, guardhost.ReceiverConfig{
		Environment: c.env,
		Identity:    guardhost.Identity{DID: s.ToolHost, KeyID: s.ToolHost + "#signing-1", Transport: c.signer, Result: c.signer},
		KEM:         c.signer.KEM(), Issuer: s.Agent, IssuerKey: s.Agent + "#signing-1",
		Approved:   c.approved,
		Bounds:     g.MCPHostBounds{Capacity: 2, Preparations: 2, Clients: 2, Owners: 4, Workers: 1, Request: 20 * time.Second, Claim: 10 * time.Second, Worker: 5 * time.Second, Client: 20 * time.Second, Tick: time.Millisecond},
		Connection: connection(o, g.MCPResponder, "sage demo tool host"),
	})
	if err != nil {
		c.factory.Close()
		return nil, err
	}
	return &ToolHostServer{host: host, factory: c.factory}, nil
}

// Serve accepts connections on l until ctx ends.
func (h *ToolHostServer) Serve(ctx context.Context, l net.Listener) error {
	err := h.host.Serve(ctx, l, 1)
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// Close closes the receiver host.
func (h *ToolHostServer) Close(ctx context.Context) error {
	err := h.host.Close(ctx)
	h.factory.Close()
	return err
}
