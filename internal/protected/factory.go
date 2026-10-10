// SPDX-License-Identifier: LGPL-3.0-or-later
package protected

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"

	b "github.com/sage-x-project/sage-adk/core/guardbinding"
	g "github.com/sage-x-project/sage/pkg/agent/guard010"

	"github.com/sage-x-project/sage-demo/internal/transfer"
)

// ConfigurationPath is the approved transfer tool configuration artifact.
const ConfigurationPath = "transfer.json"

// Measurement checks the loaded tool against its approved snapshot. The demo
// only has AcceptAll, which measures nothing.
type Measurement interface {
	Check(context.Context, *b.Snapshot) error
}

// AcceptAll accepts any snapshot. It is not a loaded-code or isolation
// measurement; MeasuredComponent stays NOT_BOUND in the demo.
type AcceptAll struct{}

// Check accepts any non-nil snapshot while ctx is active.
func (AcceptAll) Check(ctx context.Context, s *b.Snapshot) error {
	if ctx.Err() != nil || s == nil {
		return b.ErrDenied
	}
	return nil
}

// Factory loads the transfer tool into the approved operation. A factory
// without a ledger (the agent's) loads an instance that never executes.
type Factory struct {
	mu          sync.Mutex
	closed      bool
	ledger      *transfer.Ledger
	measurement Measurement
	// latest is the most recently loaded instance, kept for inspection.
	// guardbinding loads one instance per operation, so older ones are
	// owned by their (closed) operations.
	latest *instance
}

var _ b.Factory = (*Factory)(nil)

// NewFactory returns a factory for ledger, which may be nil for the agent.
func NewFactory(ledger *transfer.Ledger, m Measurement) (*Factory, error) {
	if m == nil {
		return nil, b.ErrDenied
	}
	return &Factory{ledger: ledger, measurement: m}, nil
}

type configuration struct {
	ImagePath string `json:"image_path"`
	Tool      string `json:"tool"`
	Version   string `json:"version"`
}

// Load checks that the approved manifest covers the tool configuration and
// image, that the policy covers the image, and runs the measurement.
func (f *Factory) Load(ctx context.Context, s *b.Snapshot) (b.Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed || s == nil || ctx.Err() != nil {
		return nil, b.ErrDenied
	}
	policy, err := g.PolicyCommitment(s.Policy())
	if err != nil {
		return nil, b.ErrDenied
	}
	manifest, err := g.ManifestCommitment(s.Manifest())
	if err != nil {
		return nil, b.ErrDenied
	}
	artifacts := map[string][]byte{}
	for _, a := range s.Artifacts() {
		artifacts[a.Path] = a.Bytes
	}
	var c configuration
	raw, ok := artifacts[ConfigurationPath]
	if !ok || strictJSON(raw, &c) != nil || c.Version != "0.10.0" || c.Tool != transfer.Tool || artifacts[c.ImagePath] == nil {
		return nil, b.ErrDenied
	}
	if !covers(s.Manifest(), ConfigurationPath, c.ImagePath) || !coversPolicy(s.Policy(), c.ImagePath) {
		return nil, b.ErrDenied
	}
	if f.measurement.Check(ctx, s) != nil {
		return nil, b.ErrDenied
	}
	i := &instance{owner: f, policy: policy, manifest: manifest, snapshot: s}
	f.latest = i
	return i, nil
}

// Close retires every instance; later calls are refused.
func (f *Factory) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	f.latest = nil
}

type instance struct {
	owner            *Factory
	policy, manifest string
	snapshot         *b.Snapshot
}

func (i *instance) usable(ctx context.Context) bool {
	i.owner.mu.Lock()
	closed := i.owner.closed
	i.owner.mu.Unlock()
	return !closed && ctx.Err() == nil && i.owner.measurement.Check(ctx, i.snapshot) == nil
}

// Check binds the approved commitments and tool name to this instance.
func (i *instance) Check(ctx context.Context, policy, manifest, tool string) error {
	if policy != i.policy || manifest != i.manifest || tool != transfer.Tool || !i.usable(ctx) {
		return b.ErrDenied
	}
	return nil
}

// Execute runs the transfer for exact canonical arguments and returns the
// canonical result.
func (i *instance) Execute(ctx context.Context, raw []byte) ([]byte, error) {
	canonical, err := g.Canonicalize(raw)
	if err != nil || !bytes.Equal(canonical, raw) || i.owner.ledger == nil || !i.usable(ctx) {
		return nil, b.ErrDenied
	}
	out, err := i.owner.ledger.Apply(raw)
	if err != nil {
		return nil, b.ErrDenied
	}
	return g.Canonicalize(out)
}

func covers(descriptor []byte, paths ...string) bool {
	var d struct {
		Files []struct {
			Path string `json:"path"`
		} `json:"files"`
	}
	if json.Unmarshal(descriptor, &d) != nil {
		return false
	}
	seen := map[string]bool{}
	for _, f := range d.Files {
		seen[f.Path] = true
	}
	for _, p := range paths {
		if !seen[p] {
			return false
		}
	}
	return true
}

func coversPolicy(policy []byte, paths ...string) bool {
	var p struct {
		Engine    string          `json:"engine"`
		Artifacts json.RawMessage `json:"artifacts"`
	}
	return json.Unmarshal(policy, &p) == nil && p.Engine == b.Engine && covers(p.Artifacts, paths...)
}

func strictJSON(raw []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(v)
}
