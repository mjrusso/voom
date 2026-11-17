package vm

import (
	"context"
	"io"
	"os"
	"testing"

	"github.com/mjrusso/voom/internal/process"
)

func TestRestartReplacesDriverAndGateway(t *testing.T) {
	st, m, vm := newForwardLifecycleVM(t)
	ctx := context.Background()
	t.Cleanup(func() { _, _ = m.Stop(ctx, vm.Name) })
	if _, err := m.Start(ctx, io.Discard, vm.Name); err != nil {
		t.Fatal(err)
	}
	rt := st.Runtime(vm)
	driver, err := process.RecordFingerprint(rt.VMProcessRecord())
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := process.RecordFingerprint(rt.GVProxyProcessRecord())
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := m.Restart(ctx, io.Discard, vm.Name)
	if err != nil || restarted.ID != vm.ID {
		t.Fatalf("restart changed identity: %+v, %v", restarted, err)
	}
	nextDriver, err := process.RecordFingerprint(rt.VMProcessRecord())
	if err != nil {
		t.Fatal(err)
	}
	nextGateway, err := process.RecordFingerprint(rt.GVProxyProcessRecord())
	if err != nil {
		t.Fatal(err)
	}
	if nextDriver == driver || nextGateway == gateway {
		t.Fatal("restart retained an original runtime process")
	}
}

func TestRestartReturnsStopErrorBeforeStarting(t *testing.T) {
	st := lifecycleTestStore(t)
	vm, err := st.LoadVM("a")
	if err != nil {
		t.Fatal(err)
	}
	rt := st.Runtime(vm)
	if err := os.MkdirAll(rt.Dir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rt.VMProcessRecord(), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	m := New(st)
	_, stopErr := m.Stop(context.Background(), vm.Name)
	_, restartErr := m.Restart(context.Background(), io.Discard, vm.Name)
	if stopErr == nil || restartErr == nil || restartErr.Error() != stopErr.Error() {
		t.Fatalf("restart lost stop error: stop=%v restart=%v", stopErr, restartErr)
	}
}
