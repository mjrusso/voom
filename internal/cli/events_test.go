package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/mjrusso/voom/internal/events"
)

func TestEventFilters(t *testing.T) {
	filters, err := parseEventFilters([]string{"type=forward", "type=vm", "event=install", "vm=demo"})
	if err != nil {
		t.Fatal(err)
	}
	matching := events.Event{Type: "forward", Action: "install", Actor: events.Actor{Attributes: map[string]string{"vm": "demo"}}}
	if !filters.match(matching) {
		t.Fatal("expected event to match")
	}
	matching.Action = "skip"
	if filters.match(matching) {
		t.Fatal("unexpected event match")
	}
}

func TestEventsCommandReplaysJSON(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "cache")
	t.Setenv("VOOM_CACHE_DIR", cache)
	emitter := events.NewEmitter(cache, nil)
	emitter.Emit(events.Event{Type: "vm", Action: "start", Actor: events.Actor{ID: "vm1", Attributes: map[string]string{"name": "demo"}}})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd := NewRootCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--output", "json", "events", "--since", "1"})
	if err := cmd.ExecuteContext(ctx); err != nil {
		t.Fatal(err)
	}
	var got events.Event
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output %q: %v", out.String(), err)
	}
	if got.Type != "vm" || got.Action != "start" {
		t.Fatalf("event = %#v", got)
	}
}
