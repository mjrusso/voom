package vm

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mjrusso/voom/internal/state"
	"github.com/mjrusso/voom/internal/usb"
)

func TestAddAndRemoveUSBDeviceWhileRunning(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("USB passthrough is Linux-only")
	}
	st, mgr, vmRec := runningUSBTestVM(t, nil)
	scans := 0
	mgr.scanUSB = func() (usbInventory, error) {
		scans++
		if scans > 2 {
			return nil, errors.New("USB inventory unavailable")
		}
		return testUSBInventory{}, nil
	}
	rt := st.Runtime(vmRec)
	active := serveUSBMonitor(t, rt.QEMUMonitor())

	if _, err := mgr.AddUSBDevice(context.Background(), "dev", "board", "usb-test-controller@2-255"); err != nil {
		t.Fatal(err)
	}
	loaded, err := st.LoadVM("dev")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.USBDevices) != 1 || loaded.USBDevices[0].Name != "board" {
		t.Fatalf("USB devices after add = %#v", loaded.USBDevices)
	}
	if scans != 1 {
		t.Fatalf("inventory scans after add = %d, want 1", scans)
	}
	status := mgr.ObserveUSB(context.Background(), loaded)
	if len(status) != 1 || status[0].Host.State != USBHostDisconnected || status[0].Runtime.State != USBRuntimeActive {
		t.Fatalf("USB status = %#v", status)
	}
	if scans != 2 {
		t.Fatalf("inventory scans after observation = %d, want 2", scans)
	}
	if err := mgr.RemoveUSBDevice(context.Background(), "dev", "board"); err != nil {
		t.Fatal(err)
	}
	loaded, err = st.LoadVM("dev")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.USBDevices) != 0 {
		t.Fatalf("USB devices after remove = %#v", loaded.USBDevices)
	}
	if active.Load() {
		t.Fatal("USB device remained active after removal")
	}
	if scans != 2 {
		t.Fatalf("inventory scans after removal = %d, want 2", scans)
	}
}

func TestObserveUSBSeparatesRuntimeErrors(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("USB passthrough is Linux-only")
	}
	device := usb.Decl{Name: "board", Route: usb.Route{Controller: "test-controller", Protocol: 2, Port: "255"}}
	_, mgr, vmRec := runningUSBTestVM(t, []usb.Decl{device})

	status := mgr.ObserveUSB(context.Background(), vmRec)
	if len(status) != 1 {
		t.Fatalf("USB status = %#v", status)
	}
	if status[0].Host.State != USBHostDisconnected || status[0].Host.Error != "" {
		t.Fatalf("host status = %#v", status[0].Host)
	}
	if status[0].Runtime.State != USBRuntimeUnknown || status[0].Runtime.Error == "" {
		t.Fatalf("runtime status = %#v", status[0].Runtime)
	}
}

func TestAddUSBDeviceRollsBackAfterUnobservableAttach(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("USB passthrough is Linux-only")
	}
	st, mgr, vmRec := runningUSBTestVM(t, nil)
	var active atomic.Bool
	var queries atomic.Int32
	serveUSBQMPActions(t, st.Runtime(vmRec).QEMUMonitor(), func(request vmQMPRequest) vmQMPAction {
		switch request.Execute {
		case "qom-list":
			query := queries.Add(1)
			if query == 2 {
				return vmQMPAction{drop: true}
			}
			return vmQMPAction{result: vmUSBProperties(active.Load())}
		case "device_add":
			active.Store(true)
			return vmQMPAction{drop: true}
		case "device_del":
			active.Store(false)
			return vmQMPAction{events: []vmQMPEvent{vmDeviceDeletedEvent("voom-usb-board")}}
		}
		return vmQMPAction{}
	})

	if _, err := mgr.AddUSBDevice(context.Background(), "dev", "board", "usb-test-controller@2-255"); err == nil {
		t.Fatal("ambiguous USB attach succeeded")
	}
	loaded, err := st.LoadVM("dev")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.USBDevices) != 0 || active.Load() {
		t.Fatalf("USB rollback left state=%#v active=%t", loaded.USBDevices, active.Load())
	}
}

func TestRemoveUSBDeviceRetainsAssignmentAfterUnobservableDetach(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("USB passthrough is Linux-only")
	}
	device := usb.Decl{Name: "board", Route: usb.Route{Controller: "test-controller", Protocol: 2, Port: "255"}}
	st, mgr, vmRec := runningUSBTestVM(t, []usb.Decl{device})
	var active atomic.Bool
	active.Store(true)
	var queries atomic.Int32
	serveUSBQMPActions(t, st.Runtime(vmRec).QEMUMonitor(), func(request vmQMPRequest) vmQMPAction {
		if request.Execute == "qom-list" {
			query := queries.Add(1)
			if query == 2 {
				return vmQMPAction{drop: true}
			}
			return vmQMPAction{result: vmUSBProperties(active.Load())}
		}
		if request.Execute == "device_del" {
			active.Store(false)
			return vmQMPAction{drop: true}
		}
		return vmQMPAction{}
	})

	err := mgr.RemoveUSBDevice(context.Background(), "dev", "board")
	if err == nil || !strings.Contains(err.Error(), "remains reserved") || !strings.Contains(err.Error(), "retry the removal") {
		t.Fatalf("ambiguous USB detach error = %v", err)
	}
	loaded, err := st.LoadVM("dev")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.USBDevices) != 1 || active.Load() {
		t.Fatalf("USB removal left state=%#v active=%t", loaded.USBDevices, active.Load())
	}
	if !mgr.IsRunning(vmRec) {
		t.Fatal("VM stopped after an unconfirmed USB deletion")
	}
	if err := mgr.RemoveUSBDevice(context.Background(), "dev", "board"); err != nil {
		t.Fatalf("retry USB removal: %v", err)
	}
	loaded, err = st.LoadVM("dev")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.USBDevices) != 0 {
		t.Fatalf("USB assignment remained after retry: %#v", loaded.USBDevices)
	}
}

func TestRemoveUSBDeviceRetainsAssignmentAfterPendingDeletion(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("USB passthrough is Linux-only")
	}
	device := usb.Decl{Name: "board", Route: usb.Route{Controller: "test-controller", Protocol: 2, Port: "255"}}
	st, mgr, vmRec := runningUSBTestVM(t, []usb.Decl{device})
	rt := st.Runtime(vmRec)

	serveUSBQMPActions(t, rt.QEMUMonitor(), func(request vmQMPRequest) vmQMPAction {
		switch request.Execute {
		case "qom-list":
			return vmQMPAction{result: vmUSBProperties(true)}
		case "device_del":
			return vmQMPAction{dropAfter: true}
		}
		return vmQMPAction{}
	})

	if err := mgr.RemoveUSBDevice(context.Background(), "dev", "board"); err == nil || !strings.Contains(err.Error(), "remains reserved") {
		t.Fatalf("pending USB deletion error = %v", err)
	}
	loaded, err := st.LoadVM("dev")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.USBDevices) != 1 {
		t.Fatalf("USB assignment was removed after a pending deletion: %#v", loaded.USBDevices)
	}
	if !mgr.IsRunning(vmRec) {
		t.Fatal("VM stopped after an unconfirmed USB deletion")
	}
}

func TestRemoveUSBDeviceRetainsAssignmentAfterRejectedDetach(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("USB passthrough is Linux-only")
	}
	device := usb.Decl{Name: "board", Route: usb.Route{Controller: "test-controller", Protocol: 2, Port: "255"}}
	st, mgr, vmRec := runningUSBTestVM(t, []usb.Decl{device})
	serveUSBQMPActions(t, st.Runtime(vmRec).QEMUMonitor(), func(request vmQMPRequest) vmQMPAction {
		switch request.Execute {
		case "qom-list":
			return vmQMPAction{result: vmUSBProperties(true)}
		case "device_del":
			return vmQMPAction{events: []vmQMPEvent{{name: "DEVICE_UNPLUG_GUEST_ERROR", data: map[string]any{"device": "voom-usb-board"}}}}
		}
		return vmQMPAction{}
	})

	if err := mgr.RemoveUSBDevice(context.Background(), "dev", "board"); err == nil {
		t.Fatal("rejected USB detach succeeded")
	}
	loaded, err := st.LoadVM("dev")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.USBDevices) != 1 || !mgr.IsRunning(vmRec) {
		t.Fatalf("rejected detach changed state=%#v or stopped the VM", loaded.USBDevices)
	}
}

func TestRemoveUSBDeviceCanRetryAfterStateWriteFails(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("USB passthrough is Linux-only")
	}
	device := usb.Decl{Name: "board", Route: usb.Route{Controller: "test-controller", Protocol: 2, Port: "255"}}
	st, mgr, vmRec := runningUSBTestVM(t, []usb.Decl{device})
	var active atomic.Bool
	active.Store(true)
	stateDir := st.VMDir(vmRec.ID)
	backupDir := stateDir + ".backup"
	writeBlocked := make(chan error, 1)
	serveUSBQMPActions(t, st.Runtime(vmRec).QEMUMonitor(), func(request vmQMPRequest) vmQMPAction {
		switch request.Execute {
		case "qom-list":
			return vmQMPAction{result: vmUSBProperties(active.Load())}
		case "device_del":
			active.Store(false)
			err := os.Rename(stateDir, backupDir)
			if err == nil {
				err = os.WriteFile(stateDir, nil, 0o600)
			}
			writeBlocked <- err
			return vmQMPAction{events: []vmQMPEvent{vmDeviceDeletedEvent("voom-usb-board")}}
		}
		return vmQMPAction{}
	})

	err := mgr.RemoveUSBDevice(context.Background(), "dev", "board")
	if blockErr := <-writeBlocked; blockErr != nil {
		t.Fatal(blockErr)
	}
	if err == nil || !strings.Contains(err.Error(), `QEMU detached USB device "board" from VM "dev", but Voom could not save the removal. Retry the removal`) {
		t.Fatalf("RemoveUSBDevice state-write error = %v", err)
	}
	if active.Load() {
		t.Fatal("USB device remained active after confirmed removal")
	}
	if !mgr.IsRunning(vmRec) {
		t.Fatal("VM stopped after USB state-write failure")
	}
	if err := os.Remove(stateDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backupDir, stateDir); err != nil {
		t.Fatal(err)
	}
	loaded, err := st.LoadVM("dev")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.USBDevices) != 1 {
		t.Fatalf("USB assignment was removed after failed state write: %#v", loaded.USBDevices)
	}
	if err := mgr.RemoveUSBDevice(context.Background(), "dev", "board"); err != nil {
		t.Fatalf("retry USB removal: %v", err)
	}
	loaded, err = st.LoadVM("dev")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.USBDevices) != 0 {
		t.Fatalf("USB assignment remained after retry: %#v", loaded.USBDevices)
	}
}

func TestLiveUSBRemovalRetainsReservationAfterUnconfirmedDetach(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("USB passthrough is Linux-only")
	}
	device := usb.Decl{Name: "board", Route: usb.Route{Controller: "test-controller", Protocol: 2, Port: "255"}}
	st, _ := newTestStore(t)
	vms := []*state.VMRecord{
		{SchemaVersion: state.SchemaVersion, ID: "vm1", Name: "one", Driver: "qemu", USBDevices: []usb.Decl{device}},
		{SchemaVersion: state.SchemaVersion, ID: "vm2", Name: "two", Driver: "qemu"},
	}
	for i, vmRec := range vms {
		if err := os.MkdirAll(st.VMDir(vmRec.ID), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := st.SaveVM(vmRec); err != nil {
			t.Fatal(err)
		}
		if err := st.RegisterVM(vmRec.Name, vmRec.ID); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(st.Runtime(vmRec).Dir(), 0o755); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			driver := fakeNamedVMProcess(t, "qemu-system-test")
			writeProcessRecord(t, st.Runtime(vmRec).VMProcessRecord(), driver)
		}
	}
	secondStore, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	firstManager := New(st)
	secondManager := New(secondStore)
	for _, manager := range []*Manager{firstManager, secondManager} {
		manager.scanUSB = scanTestUSB
	}

	deleteEntered := make(chan struct{})
	releaseDelete := make(chan struct{})
	serveUSBQMPActions(t, st.Runtime(vms[0]).QEMUMonitor(), func(request vmQMPRequest) vmQMPAction {
		switch request.Execute {
		case "qom-list":
			return vmQMPAction{result: vmUSBProperties(true)}
		case "device_del":
			close(deleteEntered)
			<-releaseDelete
			return vmQMPAction{dropAfter: true}
		}
		return vmQMPAction{}
	})

	removeResult := make(chan error, 1)
	go func() {
		removeResult <- firstManager.RemoveUSBDevice(context.Background(), "one", "board")
	}()
	<-deleteEntered
	pending, err := st.LoadVM("one")
	if err != nil || len(pending.USBDevices) != 1 {
		close(releaseDelete)
		<-removeResult
		t.Fatalf("pending USB removal released reservation: state=%#v error=%v", pending, err)
	}
	globalResult := make(chan error, 1)
	go func() {
		globalResult <- secondStore.WithGlobal(func() error { return nil })
	}()
	select {
	case err := <-globalResult:
		if err != nil {
			close(releaseDelete)
			<-removeResult
			t.Fatalf("acquire global state lock during USB removal: %v", err)
		}
	case <-time.After(time.Second):
		close(releaseDelete)
		<-removeResult
		t.Fatal("USB removal held the global state lock while waiting for QMP")
	}

	addResult := make(chan error, 1)
	go func() {
		_, err := secondManager.AddUSBDevice(context.Background(), "two", "probe", device.Location())
		addResult <- err
	}()
	select {
	case err := <-addResult:
		close(releaseDelete)
		<-removeResult
		t.Fatalf("competing assignment completed before removal returned: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseDelete)
	if err := <-removeResult; err == nil || !strings.Contains(err.Error(), "remains reserved") {
		t.Fatalf("USB removal after lost monitor response = %v", err)
	}
	if err := <-addResult; err != nil {
		t.Fatalf("assignment to stopped VM failed after removal returned: %v", err)
	}
	for _, name := range []string{"one", "two"} {
		loaded, err := st.LoadVM(name)
		if err != nil {
			t.Fatal(err)
		}
		if len(loaded.USBDevices) != 1 {
			t.Fatalf("%s USB devices = %#v", name, loaded.USBDevices)
		}
	}
	if !firstManager.IsRunning(vms[0]) {
		t.Fatal("VM stopped after an unconfirmed USB deletion")
	}
	secondVM, err := secondStore.LoadVM("two")
	if err != nil {
		t.Fatal(err)
	}
	err = secondManager.withUSBStartReservation(context.Background(), secondVM, func() error {
		t.Fatal("conflicting VM start was allowed")
		return nil
	})
	if err == nil {
		t.Fatal("unconfirmed USB deletion released the route reservation")
	}
}

func TestUnconfirmedUSBRuntimeRetainsRouteReservation(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("USB passthrough is Linux-only")
	}
	device := usb.Decl{Name: "board", Route: usb.Route{Controller: "test-controller", Protocol: 2, Port: "255"}}
	st, _ := newTestStore(t)
	vms := []*state.VMRecord{
		{SchemaVersion: state.SchemaVersion, ID: "vm1", Name: "one", Driver: "qemu", USBDevices: []usb.Decl{device}},
		{SchemaVersion: state.SchemaVersion, ID: "vm2", Name: "two", Driver: "qemu", USBDevices: []usb.Decl{{Name: "probe", Route: device.Route}}},
	}
	for _, vmRec := range vms {
		if err := os.MkdirAll(st.VMDir(vmRec.ID), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := st.SaveVM(vmRec); err != nil {
			t.Fatal(err)
		}
		if err := st.RegisterVM(vmRec.Name, vmRec.ID); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(st.Runtime(vmRec).Dir(), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeProcessRecord(t, st.Runtime(vms[0]).VMProcessRecord(), fakeNamedVMProcess(t, "qemu-system-test"))
	firstManager := New(st)
	firstManager.scanUSB = scanTestUSB
	corruptResult := make(chan error, 1)
	serveUSBQMPActions(t, st.Runtime(vms[0]).QEMUMonitor(), func(request vmQMPRequest) vmQMPAction {
		switch request.Execute {
		case "qom-list":
			return vmQMPAction{result: vmUSBProperties(true)}
		case "device_del":
			corruptResult <- os.WriteFile(st.Runtime(vms[0]).VMProcessRecord(), []byte("invalid\n"), 0o600)
			return vmQMPAction{dropAfter: true}
		}
		return vmQMPAction{}
	})

	if err := firstManager.RemoveUSBDevice(context.Background(), "one", "board"); err == nil {
		t.Fatal("USB removal succeeded despite an unconfirmed runtime change")
	}
	if err := <-corruptResult; err != nil {
		t.Fatal(err)
	}
	loaded, err := st.LoadVM("one")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.USBDevices) != 1 {
		t.Fatalf("unconfirmed VM lost USB reservation: %#v", loaded.USBDevices)
	}
	if err := firstManager.RemoveUSBDevice(context.Background(), "one", "board"); err == nil {
		t.Fatal("USB assignment changed while the VM runtime remained unconfirmed")
	}
	loaded, err = st.LoadVM("one")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.USBDevices) != 1 {
		t.Fatalf("retry removed the unconfirmed VM reservation: %#v", loaded.USBDevices)
	}

	secondStore, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	secondVM, err := secondStore.LoadVM("two")
	if err != nil {
		t.Fatal(err)
	}
	err = New(secondStore).withUSBStartReservation(context.Background(), secondVM, func() error {
		t.Fatal("conflicting VM start was allowed")
		return nil
	})
	if err == nil {
		t.Fatal("unconfirmed VM did not retain the route reservation")
	}
}

func runningUSBTestVM(t *testing.T, devices []usb.Decl) (*state.Store, *Manager, *state.VMRecord) {
	t.Helper()
	st, mgr, vmRec, _ := runningUSBTestVMWithDriver(t, devices)
	return st, mgr, vmRec
}

func runningUSBTestVMWithDriver(t *testing.T, devices []usb.Decl) (*state.Store, *Manager, *state.VMRecord, *os.Process) {
	t.Helper()
	st, _ := newTestStore(t)
	vmRec := &state.VMRecord{SchemaVersion: state.SchemaVersion, ID: "vm1", Name: "dev", Driver: "qemu", USBDevices: devices}
	if err := os.MkdirAll(st.VMDir(vmRec.ID), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveVM(vmRec); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterVM(vmRec.Name, vmRec.ID); err != nil {
		t.Fatal(err)
	}
	rt := st.Runtime(vmRec)
	if err := os.MkdirAll(rt.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	driver := fakeNamedVMProcess(t, "qemu-system-test")
	writeProcessRecord(t, rt.VMProcessRecord(), driver)
	mgr := New(st)
	mgr.scanUSB = scanTestUSB
	return st, mgr, vmRec, driver.Process
}

type testUSBInventory struct {
	resolveErr error
}

func (testUSBInventory) Inspect(usb.Decl) (usb.Device, bool, error) {
	return usb.Device{}, false, nil
}

func (i testUSBInventory) Resolve(device usb.Decl) (usb.Binding, error) {
	if i.resolveErr != nil {
		return usb.Binding{}, i.resolveErr
	}
	return usb.Binding{Name: device.Name, Bus: 255, Port: device.Port}, nil
}

func scanTestUSB() (usbInventory, error) {
	return testUSBInventory{}, nil
}

func serveUSBMonitor(t *testing.T, socket string) *atomic.Bool {
	t.Helper()
	active := &atomic.Bool{}
	serveUSBQMPActions(t, socket, func(request vmQMPRequest) vmQMPAction {
		switch request.Execute {
		case "qom-list":
			return vmQMPAction{result: vmUSBProperties(active.Load())}
		case "device_add":
			active.Store(true)
		case "device_del":
			active.Store(false)
			return vmQMPAction{events: []vmQMPEvent{vmDeviceDeletedEvent("voom-usb-board")}}
		}
		return vmQMPAction{}
	})
	return active
}

type vmQMPRequest struct {
	Execute   string         `json:"execute"`
	Arguments map[string]any `json:"arguments,omitempty"`
	ID        uint64         `json:"id"`
}

type vmQMPEvent struct {
	name string
	data any
}

type vmQMPAction struct {
	result    any
	events    []vmQMPEvent
	drop      bool
	dropAfter bool
}

func vmUSBProperties(active bool) []map[string]string {
	properties := []map[string]string{{"name": "voom-xhci", "type": "child<xHCI>"}}
	if active {
		properties = append(properties, map[string]string{"name": "voom-usb-board", "type": "child<usb-host>"})
	}
	return properties
}

func vmDeviceDeletedEvent(id string) vmQMPEvent {
	return vmQMPEvent{name: "DEVICE_DELETED", data: map[string]any{"device": id, "path": "/machine/peripheral/" + id}}
}

func serveUSBQMPActions(t *testing.T, socket string, respond func(vmQMPRequest) vmQMPAction) {
	t.Helper()
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			serveVMQMPConnection(conn, respond)
		}
	}()
}

func serveVMQMPConnection(conn net.Conn, respond func(vmQMPRequest) vmQMPAction) {
	defer func() { _ = conn.Close() }()
	encoder := json.NewEncoder(conn)
	decoder := json.NewDecoder(conn)
	_ = encoder.Encode(map[string]any{"QMP": map[string]any{"version": map[string]any{}, "capabilities": []string{}}})
	for {
		var request vmQMPRequest
		if err := decoder.Decode(&request); err != nil {
			return
		}
		if request.Execute == "qmp_capabilities" {
			_ = encoder.Encode(map[string]any{"return": map[string]any{}, "id": request.ID})
			continue
		}
		action := respond(request)
		if action.drop {
			return
		}
		result := action.result
		if result == nil {
			result = map[string]any{}
		}
		_ = encoder.Encode(map[string]any{"return": result, "id": request.ID})
		for _, event := range action.events {
			_ = encoder.Encode(map[string]any{"event": event.name, "data": event.data})
		}
		if action.dropAfter {
			return
		}
	}
}
