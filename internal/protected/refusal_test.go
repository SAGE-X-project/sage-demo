// SPDX-License-Identifier: LGPL-3.0-or-later
package protected

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	s "github.com/sage-x-project/sage-demo/internal/scenario"
	"github.com/sage-x-project/sage-demo/internal/transfer"
)

func TestSetupRefusesExistingOrLongDirectory(t *testing.T) {
	if err := Setup(t.TempDir()); err == nil {
		t.Fatal("Setup reused an existing directory")
	}
	if err := Setup("/tmp/" + strings.Repeat("x", maxSocket)); err == nil {
		t.Fatal("Setup accepted a directory too long for signer sockets")
	}
}

func TestFactoryRefusals(t *testing.T) {
	if _, err := NewFactory(nil, nil); err == nil {
		t.Fatal("NewFactory without measurement succeeded")
	}
	f, err := NewFactory(nil, AcceptAll{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Load(context.Background(), nil); err == nil {
		t.Fatal("Load(nil) succeeded")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if (AcceptAll{}).Check(ctx, nil) == nil {
		t.Fatal("AcceptAll accepted a cancelled check")
	}
	if covers([]byte(`{`), "x") || coversPolicy([]byte(`{"engine":"other","artifacts":{"files":[]}}`)) {
		t.Fatal("malformed descriptors were accepted")
	}
}

// The loaded instance only executes exact canonical transfer arguments for
// its own commitments, and nothing after the factory closes.
func TestInstanceRefusals(t *testing.T) {
	w := newWorld(t)
	f := w.host.factory
	f.mu.Lock()
	i := f.latest
	f.mu.Unlock()
	if i == nil {
		t.Fatal("no loaded instance")
	}
	ctx := context.Background()
	if i.Check(ctx, i.policy, i.manifest, transfer.Tool) != nil {
		t.Fatal("Check refused its own commitments")
	}
	for name, args := range map[string][3]string{
		"other policy":   {strings.Repeat("0", 64), i.manifest, transfer.Tool},
		"other manifest": {i.policy, strings.Repeat("0", 64), transfer.Tool},
		"other tool":     {i.policy, i.manifest, "calculator"},
	} {
		if i.Check(ctx, args[0], args[1], args[2]) == nil {
			t.Errorf("%s: Check succeeded", name)
		}
	}
	for name, raw := range map[string]string{
		"not canonical":    `{"to":"did:sage:web:agent.example:carol","amount":10,"currency":"DEMO"}`,
		"invalid transfer": `{"amount":0,"currency":"DEMO","to":"did:sage:web:agent.example:carol"}`,
		"not json":         `{`,
	} {
		if _, err := i.Execute(ctx, []byte(raw)); err == nil {
			t.Errorf("%s: Execute succeeded", name)
		}
	}
	noLedger := &instance{owner: &Factory{measurement: AcceptAll{}}, snapshot: i.snapshot}
	if _, err := noLedger.Execute(ctx, s.Approved); err == nil {
		t.Fatal("an instance without a ledger executed")
	}
	if got := w.entries(t); len(got) != 0 {
		t.Fatalf("ledger = %+v", got)
	}
	closed := &Factory{measurement: AcceptAll{}, closed: true}
	if _, err := closed.Load(ctx, i.snapshot); err == nil {
		t.Fatal("a closed factory loaded")
	}
	shut := &instance{owner: closed, policy: i.policy, manifest: i.manifest, snapshot: i.snapshot}
	if shut.Check(ctx, i.policy, i.manifest, transfer.Tool) == nil {
		t.Fatal("an instance of a closed factory passed Check")
	}
}

func TestHostAssemblyRefusals(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "sd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	dir := filepath.Join(root, "d")
	if err := Setup(dir); err != nil {
		t.Fatal(err)
	}
	clock := &testClock{started: time.Now()}
	o := Options{Dir: dir, Create: true, Clock: clock, SignerUID: uint32(os.Getuid()), Timeout: time.Second}
	ctx := context.Background()
	ledger, err := transfer.OpenLedger(Layout{Dir: dir}.Ledger("refusal"), s.OpeningBalance)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := OpenToolHost(ctx, o, nil); err == nil {
		t.Fatal("OpenToolHost without a ledger succeeded")
	}
	if _, err := OpenAgent(ctx, Options{Dir: dir}); err == nil {
		t.Fatal("OpenAgent without a clock succeeded")
	}
	// No signer is serving, so neither host can check its identity.
	if _, err := OpenAgent(ctx, o); err == nil {
		t.Fatal("OpenAgent without a signer succeeded")
	}
	if _, err := OpenToolHost(ctx, o, ledger); err == nil {
		t.Fatal("OpenToolHost without a signer succeeded")
	}
	if err := ServeSigner(ctx, o, Role("carol"), 0, nil); err == nil {
		t.Fatal("ServeSigner accepted an unknown role")
	}

	for _, name := range []string{"operator.pub", "approval.json", "manifest.json", "policy.json"} {
		path := Layout{Dir: dir}.Shared(name)
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenAgent(ctx, o); err == nil {
			t.Fatalf("OpenAgent with a broken %s succeeded", name)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenAgent(ctx, o); err == nil {
			t.Fatalf("OpenAgent without %s succeeded", name)
		}
	}
	if err := os.Remove(Layout{Dir: dir}.Key("bob.kem")); err != nil {
		t.Fatal(err)
	}
	if err := ServeSigner(ctx, o, ToolHost, 0, nil); err == nil {
		t.Fatal("ServeSigner without the KEM key succeeded")
	}
	if err := os.Remove(Layout{Dir: dir}.Key("alice.key")); err != nil {
		t.Fatal(err)
	}
	if err := ServeSigner(ctx, o, Agent, 0, nil); err == nil {
		t.Fatal("ServeSigner without the signing key succeeded")
	}
}

func TestSyntheticSource(t *testing.T) {
	dir := t.TempDir()
	clock := &testClock{started: time.Now()}
	src := fileSource{path: filepath.Join(dir, "registry.json"), clock: clock}
	if _, err := src.Read(context.Background(), s.Agent); err == nil {
		t.Fatal("Read without a registry file succeeded")
	}
	if err := os.WriteFile(src.path, []byte(`{"signing":{"`+s.Agent+`":"aa"},"kem":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Read(context.Background(), s.ToolHost); err == nil {
		t.Fatal("Read of an unknown identity succeeded")
	}
	snap, err := src.Read(context.Background(), s.Agent)
	if err != nil || snap.Source != SourceName || len(snap.Keys) != 1 {
		t.Fatalf("Read = %+v, %v", snap, err)
	}
}
