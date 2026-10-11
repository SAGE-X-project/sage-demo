// SPDX-License-Identifier: LGPL-3.0-or-later

// Package protected is the SAGE run of the demo. The agent and the tool host
// are sage-adk guardhost caller and receiver hosts; signing and KEM keys stay
// in guardsigner processes; the operator approves exactly one transfer.
//
// Two ports are synthetic and named as such: the Registry Source reads a
// local file whose readiness flags are asserted, and the tool measurement
// accepts any snapshot. IdentityAndReadiness and MeasuredComponent therefore
// stay NOT_BOUND, and a demo run is not Registry, measurement or deployment
// evidence.
package protected

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/sage-x-project/sage-adk/core/guardapproval"
	b "github.com/sage-x-project/sage-adk/core/guardbinding"
	"github.com/sage-x-project/sage-adk/core/guardhost"
	"github.com/sage-x-project/sage-adk/core/guardsigner"
	r "github.com/sage-x-project/sage/pkg/agent/registry010"

	s "github.com/sage-x-project/sage-demo/internal/scenario"
	"github.com/sage-x-project/sage-demo/internal/transfer"
)

// Registry configuration of the synthetic Source.
const (
	SourceName = "synthetic-demo-fixture"
	Registry   = "web:agent.example"
	Network    = "local"
)

// Role names one demo identity.
type Role string

const (
	// Agent is Alice, the caller.
	Agent Role = "alice"
	// ToolHost is Bob, the receiver.
	ToolHost Role = "bob"
)

// Layout names the files of one demo directory.
type Layout struct{ Dir string }

// Shared returns a file every host reads.
func (l Layout) Shared(name string) string { return filepath.Join(l.Dir, "shared", name) }

// Key returns a private key file, readable only by its signer.
func (l Layout) Key(name string) string { return filepath.Join(l.Dir, "keys", name) }

// Socket returns the signer socket of role.
func (l Layout) Socket(role Role) string { return filepath.Join(l.Dir, "sock", string(role)) }

// State returns the protected state directory of role.
func (l Layout) State(role Role) string { return filepath.Join(l.Dir, string(role), "state") }

// Artifacts returns the approved artifact copy of role.
func (l Layout) Artifacts(role Role) string { return filepath.Join(l.Dir, string(role), "artifacts") }

// Ledger returns the tool host's transfer ledger file for a run.
func (l Layout) Ledger(run string) string { return filepath.Join(l.Dir, "ledgers", run+".jsonl") }

// maxSocket keeps signer socket paths within the Unix socket path limit.
const maxSocket = 100

// Setup creates the keys, the approved operation, the operator approval and
// the synthetic Registry records under dir, which must not exist. The
// operator key signs the approval and is then discarded; only its public key
// is kept.
func Setup(dir string) (err error) {
	l := Layout{Dir: dir}
	if len(l.Socket(ToolHost)) > maxSocket {
		return fmt.Errorf("demo directory path too long for signer sockets: %s", dir)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("demo directory already exists: %s", dir)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(dir)
		}
	}()
	for _, d := range []string{filepath.Join(dir, "shared"), filepath.Join(dir, "keys"), filepath.Join(dir, "sock"), filepath.Join(dir, "ledgers"),
		filepath.Join(dir, string(Agent)), filepath.Join(dir, string(ToolHost)), l.State(Agent), l.State(ToolHost), l.Artifacts(Agent), l.Artifacts(ToolHost)} {
		if err := os.Mkdir(d, 0o700); err != nil {
			return err
		}
	}
	alice, err := guardsigner.GenerateKeyFile(l.Key("alice.key"))
	if err != nil {
		return err
	}
	bob, err := guardsigner.GenerateKeyFile(l.Key("bob.key"))
	if err != nil {
		return err
	}
	kem, err := guardsigner.GenerateKEMFile(l.Key("bob.kem"))
	if err != nil {
		return err
	}
	artifacts, policy, manifest, err := operation()
	if err != nil {
		return err
	}
	operatorPublic, operator, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	approval, err := guardapproval.Sign(operator, policy, manifest, 1)
	clear(operator)
	if err != nil {
		return err
	}
	registry, err := json.Marshal(registryFile{
		Signing: map[string]string{s.Agent: hex.EncodeToString(alice), s.ToolHost: hex.EncodeToString(bob)},
		KEM:     map[string]string{s.ToolHost: hex.EncodeToString(kem)},
	})
	if err != nil {
		return err
	}
	files := map[string][]byte{
		l.Shared("policy.json"):   policy,
		l.Shared("manifest.json"): manifest,
		l.Shared("approval.json"): approval,
		l.Shared("operator.pub"):  []byte(hex.EncodeToString(operatorPublic)),
		l.Shared("registry.json"): registry,
	}
	for name, data := range artifacts {
		files[filepath.Join(l.Artifacts(Agent), name)] = data
		files[filepath.Join(l.Artifacts(ToolHost), name)] = data
	}
	for path, data := range files {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// operation returns the artifacts and descriptors of the one approved
// operation: Alice may ask Bob for exactly the approved transfer.
func operation() (map[string][]byte, []byte, []byte, error) {
	rules, err := json.Marshal(map[string]any{"version": "0.10.0", "recipient": s.ToolHost, "keyid": s.Agent + "#signing-1", "tool": transfer.Tool, "arguments": json.RawMessage(s.Approved), "max_lifetime": 300})
	if err != nil {
		return nil, nil, nil, err
	}
	config, err := json.Marshal(configuration{ImagePath: "image.fixture", Tool: transfer.Tool, Version: "0.10.0"})
	if err != nil {
		return nil, nil, nil, err
	}
	artifacts := map[string][]byte{
		"rules.json":      rules,
		ConfigurationPath: config,
		"image.fixture":   []byte("synthetic demo tool image; not a runtime measurement"),
	}
	// Descriptors list files sorted by path.
	files := func(names ...string) json.RawMessage {
		slices.Sort(names)
		rows := []any{}
		for _, n := range names {
			sum := sha256.Sum256(artifacts[n])
			rows = append(rows, map[string]string{"path": n, "sha256": hex.EncodeToString(sum[:])})
		}
		out, _ := json.Marshal(map[string]any{"version": "0.10.0", "files": rows})
		return out
	}
	policy, err := json.Marshal(map[string]any{"version": "0.10.0", "issuer": s.Agent, "epoch": "00000000-0000-4000-8000-000000000001", "engine": b.Engine, "artifacts": files("image.fixture", "rules.json")})
	if err != nil {
		return nil, nil, nil, err
	}
	return artifacts, policy, files(ConfigurationPath, "image.fixture"), nil
}

// registryFile holds the synthetic Registry records: each identity's Ed25519
// signing key and, for the tool host, its X25519 KEM key.
type registryFile struct {
	Signing map[string]string `json:"signing"`
	KEM     map[string]string `json:"kem"`
}

// fileSource is the synthetic Registry Source. It reads the registry file on
// every Read and asserts the readiness flags; it observes no chain.
type fileSource struct {
	path  string
	clock guardhost.Clock
}

func (f fileSource) Read(ctx context.Context, did string) (r.Snapshot, error) {
	raw, err := os.ReadFile(f.path)
	if err != nil || ctx.Err() != nil {
		return r.Snapshot{}, errors.New("synthetic source unavailable")
	}
	var keys registryFile
	if json.Unmarshal(raw, &keys) != nil || keys.Signing[did] == "" {
		return r.Snapshot{}, errors.New("unknown identity")
	}
	list := []r.Key{{Name: "signing-1", Alg: "ed25519", Material: keys.Signing[did], State: "accepted"}}
	if kem := keys.KEM[did]; kem != "" {
		list = append([]r.Key{{Name: "kem-1", Alg: "x25519", Material: kem, State: "accepted"}}, list...)
	}
	stamp, err := f.clock.Now()
	if err != nil {
		return r.Snapshot{}, err
	}
	sum := sha256.Sum256(raw)
	return r.Snapshot{Source: SourceName, Registry: Registry, Network: Network, DID: did, Version: "1", State: "active", Digest: hex.EncodeToString(sum[:]), Ready: true, Validated: true, Finalized: true, AcquiredMS: stamp.MonoMS, Keys: list}, nil
}
