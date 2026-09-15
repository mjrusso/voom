package cli

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/mjrusso/voom/internal/egress"
	"github.com/mjrusso/voom/internal/state"
	"github.com/mjrusso/voom/internal/vm"
)

func TestEgressReplayDoesNotRetargetSocket(t *testing.T) {
	record := &state.VMRecord{Name: "source", Network: state.VMNetwork{Egress: &egress.Decl{Mode: egress.ModeExplicit, BackendSocket: "/tmp/proxy socket", CACertPath: "/tmp/CA's.pem"}}}
	command := egressReplayCommand(record)
	want := "voom config egress set source --backend-socket " + shellQuote(record.Network.Egress.BackendSocket) + " --ca-cert " + shellQuote(record.Network.Egress.CACertPath) + " --disabled"
	if command != want {
		t.Fatalf("replay: %s", command)
	}
}

func TestEgressJSONUsesOneResultShape(t *testing.T) {
	var out bytes.Buffer
	cmd := NewRootCommand()
	cmd.SetOut(&out)
	if err := cmd.PersistentFlags().Set("output", "json"); err != nil {
		t.Fatal(err)
	}
	result := vm.EgressResult{ID: "vm1", Name: "a", Changed: true, Egress: &egress.Decl{Mode: egress.ModeExplicit, Enabled: true}}
	if err := writeEgressResult(cmd, "enable", result); err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"id", "name", "changed", "runtimeChanged", "egress"} {
		if _, ok := fields[name]; !ok {
			t.Fatalf("missing %q in %s", name, out.String())
		}
	}
	if _, ok := fields["enabled"]; ok {
		t.Fatalf("redundant enabled field in %s", out.String())
	}
}
