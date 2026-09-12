package cli

import (
	"strings"
	"testing"

	"github.com/mjrusso/voom/internal/egress"
	"github.com/mjrusso/voom/internal/state"
)

func TestEgressReplayDoesNotRetargetSocket(t *testing.T) {
	record := &state.VMRecord{Name: "source", Network: state.VMNetwork{Egress: &egress.Decl{Mode: egress.ModeExplicit, BackendSocket: "/tmp/proxy socket", CACertPath: "/tmp/CA's.pem"}}}
	commands := replayCommands(record, "source", true)
	if len(commands) != 2 || !strings.Contains(commands[0], shellQuote(record.Network.Egress.BackendSocket)) || !strings.Contains(commands[0], shellQuote(record.Network.Egress.CACertPath)) || commands[1] != "voom config egress disable source" {
		t.Fatalf("replay: %v", commands)
	}
	if commands := replayCommands(record, "clone", false); len(commands) != 0 {
		t.Fatalf("clone inherited socket: %v", commands)
	}
}
