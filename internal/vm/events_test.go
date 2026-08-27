package vm

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/mjrusso/voom/internal/events"
	"github.com/mjrusso/voom/internal/state"
)

type recordingEmitter struct {
	events []events.Event
}

func TestRemoveEmitsAfterDeletion(t *testing.T) {
	st, _ := newTestStore(t)
	recorder := &recordingEmitter{}
	m := New(st, WithEventEmitter(recorder))
	record := &state.VMRecord{SchemaVersion: state.SchemaVersion, ID: "vm1", Name: "demo", Driver: "qemu", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := os.MkdirAll(st.VMDir(record.ID), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveVM(record); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterVM(record.Name, record.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove(context.Background(), record.Name); err != nil {
		t.Fatal(err)
	}
	if len(recorder.events) != 1 || recorder.events[0].Action != "rm" {
		t.Fatalf("events = %#v", recorder.events)
	}
	if _, err := st.LoadVM(record.Name); err == nil {
		t.Fatal("VM still exists when rm event was emitted")
	}
}

func (r *recordingEmitter) Emit(ev events.Event) {
	r.events = append(r.events, ev)
}

func TestEmitAutoForwardTransitions(t *testing.T) {
	recorder := &recordingEmitter{}
	m := &Manager{events: recorder}
	vm := &state.VMRecord{ID: "vm-id", Name: "demo"}
	active := RuntimeAutoForward{Protocol: "tcp", Bind: "127.0.0.1", HostPort: 8080, GuestPort: 80, Installed: true, Status: "active"}
	skipped := active
	skipped.Installed = false
	skipped.Status = "skipped"
	skipped.Reason = "busy"

	m.emitAutoForwardTransitions(vm, nil, []RuntimeAutoForward{active})
	m.emitAutoForwardTransitions(vm, []RuntimeAutoForward{active}, []RuntimeAutoForward{active})
	m.emitAutoForwardTransitions(vm, []RuntimeAutoForward{active}, []RuntimeAutoForward{skipped})
	m.emitAutoForwardTransitions(vm, []RuntimeAutoForward{skipped}, []RuntimeAutoForward{skipped})
	skipped.Reason = "reserved"
	m.emitAutoForwardTransitions(vm, []RuntimeAutoForward{{Protocol: "tcp", Bind: "127.0.0.1", HostPort: 8080, GuestPort: 80, Status: "skipped", Reason: "busy"}}, []RuntimeAutoForward{skipped})

	actions := []string{}
	for _, ev := range recorder.events {
		actions = append(actions, ev.Action)
	}
	want := []string{"install", "uninstall", "skip", "skip"}
	if len(actions) != len(want) {
		t.Fatalf("actions = %v, want %v", actions, want)
	}
	for i := range want {
		if actions[i] != want[i] {
			t.Fatalf("actions = %v, want %v", actions, want)
		}
	}
}
