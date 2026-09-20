package qemu

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mjrusso/voom/internal/usb"
)

func TestAttachAndDetachUSB(t *testing.T) {
	controller := false
	active := false
	monitor := runQMP(t, func(request qmpTestRequest) qmpTestAction {
		switch request.Execute {
		case "qom-list":
			return qmpTestAction{result: qmpProperties(controller, active)}
		case "device_add":
			switch request.Arguments["driver"] {
			case "qemu-xhci":
				controller = true
			case "usb-host":
				active = true
			}
		case "device_del":
			active = false
			return qmpTestAction{eventsAfter: []qmpTestEvent{deviceDeletedEvent("voom-usb-board")}}
		}
		return qmpTestAction{}
	})
	requireUSBOutcome(t, AttachUSB(context.Background(), monitor.socket, usbBindingForTest()), USBChangeApplied)
	requireUSBOutcome(t, DetachUSB(context.Background(), monitor.socket, usbDeclForTest()), USBChangeApplied)

	want := []string{"qom-list", "device_add", "device_add", "qom-list", "device_del"}
	requests := make([]qmpTestRequest, len(want))
	for i, command := range want {
		requests[i] = <-monitor.received
		if requests[i].Execute != command {
			t.Fatalf("command %d = %q, want %q", i, requests[i].Execute, command)
		}
	}
	controllerAdd := requests[1].Arguments
	if controllerAdd["driver"] != "qemu-xhci" || controllerAdd["id"] != "voom-xhci" {
		t.Fatalf("controller arguments = %#v", controllerAdd)
	}
	deviceAdd := requests[2].Arguments
	if deviceAdd["driver"] != "usb-host" || deviceAdd["id"] != "voom-usb-board" || deviceAdd["bus"] != "voom-xhci.0" || deviceAdd["hostbus"] != float64(1) || deviceAdd["hostport"] != "3.2" {
		t.Fatalf("USB device arguments = %#v", deviceAdd)
	}
}

func TestAttachUSBConfirmsLostCommandResponse(t *testing.T) {
	controller := false
	active := false
	monitor := runQMP(t, func(request qmpTestRequest) qmpTestAction {
		switch request.Execute {
		case "qom-list":
			return qmpTestAction{result: qmpProperties(controller, active)}
		case "device_add":
			if request.Arguments["driver"] == "qemu-xhci" {
				controller = true
				return qmpTestAction{}
			}
			active = true
			return qmpTestAction{drop: true}
		}
		return qmpTestAction{}
	})
	requireUSBOutcome(t, AttachUSB(context.Background(), monitor.socket, usbBindingForTest()), USBChangeApplied)
	for range 4 {
		<-monitor.received
	}
}

func TestAttachUSBMarksUnobservableCommandOutcome(t *testing.T) {
	queries := 0
	monitor := runQMP(t, func(request qmpTestRequest) qmpTestAction {
		if request.Execute == "qom-list" {
			queries++
			if queries == 1 {
				return qmpTestAction{result: qmpProperties(true, false)}
			}
			return qmpTestAction{drop: true}
		}
		if request.Execute == "device_add" {
			return qmpTestAction{drop: true}
		}
		return qmpTestAction{}
	})
	requireUSBOutcome(t, AttachUSB(context.Background(), monitor.socket, usbBindingForTest()), USBChangeUnknown)
}

func TestDetachUSBConfirmsLostCommandResponse(t *testing.T) {
	active := true
	monitor := runQMP(t, func(request qmpTestRequest) qmpTestAction {
		switch request.Execute {
		case "qom-list":
			return qmpTestAction{result: qmpProperties(true, active)}
		case "device_del":
			active = false
			return qmpTestAction{drop: true}
		}
		return qmpTestAction{}
	})
	requireUSBOutcome(t, DetachUSB(context.Background(), monitor.socket, usbDeclForTest()), USBChangeApplied)
	for range 3 {
		<-monitor.received
	}
}

func TestDetachUSBMarksPendingDeletionUnobservable(t *testing.T) {
	monitor := runQMP(t, func(request qmpTestRequest) qmpTestAction {
		if request.Execute == "qom-list" {
			return qmpTestAction{result: qmpProperties(true, true)}
		}
		return qmpTestAction{dropAfter: true}
	})
	requireUSBOutcome(t, DetachUSB(context.Background(), monitor.socket, usbDeclForTest()), USBChangeUnknown)
}

func TestAttachUSBReusesController(t *testing.T) {
	monitor := runQMP(t, func(request qmpTestRequest) qmpTestAction {
		if request.Execute == "qom-list" {
			return qmpTestAction{result: qmpProperties(true, false)}
		}
		return qmpTestAction{}
	})
	requireUSBOutcome(t, AttachUSB(context.Background(), monitor.socket, usbBindingForTest()), USBChangeApplied)
	if got := <-monitor.received; got.Execute != "qom-list" {
		t.Fatalf("first command = %q", got.Execute)
	}
	got := <-monitor.received
	if got.Execute != "device_add" || got.Arguments["driver"] != "usb-host" {
		t.Fatalf("second command = %#v", got)
	}
}

func TestAttachUSBReturnsStructuredQMPError(t *testing.T) {
	monitor := runQMP(t, func(request qmpTestRequest) qmpTestAction {
		if request.Execute == "qom-list" {
			return qmpTestAction{result: qmpProperties(true, false)}
		}
		return qmpTestAction{failure: &qmpFailure{Class: "GenericError", Desc: "device rejected"}}
	})
	err := requireUSBFailure(t, AttachUSB(context.Background(), monitor.socket, usbBindingForTest()), USBChangeNotApplied)
	if !strings.Contains(err.Error(), "GenericError: device rejected") {
		t.Fatalf("error = %v", err)
	}
}

func TestDetachUSBReturnsStructuredQMPError(t *testing.T) {
	monitor := runQMP(t, func(request qmpTestRequest) qmpTestAction {
		if request.Execute == "qom-list" {
			return qmpTestAction{result: qmpProperties(true, true)}
		}
		return qmpTestAction{failure: &qmpFailure{Class: "DeviceNotFound", Desc: "device not found"}}
	})
	err := requireUSBFailure(t, DetachUSB(context.Background(), monitor.socket, usbDeclForTest()), USBChangeNotApplied)
	if !strings.Contains(err.Error(), "DeviceNotFound: device not found") {
		t.Fatalf("error = %v", err)
	}
}

func TestDetachUSBReturnsGuestRejection(t *testing.T) {
	monitor := runQMP(t, func(request qmpTestRequest) qmpTestAction {
		if request.Execute == "qom-list" {
			return qmpTestAction{result: qmpProperties(true, true)}
		}
		return qmpTestAction{eventsAfter: []qmpTestEvent{{name: "DEVICE_UNPLUG_GUEST_ERROR", data: map[string]any{"device": "voom-usb-board"}}}}
	})
	err := requireUSBFailure(t, DetachUSB(context.Background(), monitor.socket, usbDeclForTest()), USBChangeNotApplied)
	if !strings.Contains(err.Error(), "guest rejected removal") {
		t.Fatalf("error = %v", err)
	}
}

func TestDetachUSBAcceptsDeletionEventBeforeResponse(t *testing.T) {
	monitor := runQMP(t, func(request qmpTestRequest) qmpTestAction {
		if request.Execute == "qom-list" {
			return qmpTestAction{result: qmpProperties(true, true)}
		}
		return qmpTestAction{eventsBefore: []qmpTestEvent{deviceDeletedEvent("voom-usb-board")}}
	})
	requireUSBOutcome(t, DetachUSB(context.Background(), monitor.socket, usbDeclForTest()), USBChangeApplied)
}

func TestDetachUSBAllowsAlreadyMissingDevice(t *testing.T) {
	monitor := runQMP(t, func(qmpTestRequest) qmpTestAction {
		return qmpTestAction{result: qmpProperties(true, false)}
	})
	requireUSBOutcome(t, DetachUSB(context.Background(), monitor.socket, usbDeclForTest()), USBChangeApplied)
	if got := <-monitor.received; got.Execute != "qom-list" {
		t.Fatalf("command = %q", got.Execute)
	}
}

func TestSystemPowerdownUsesQMP(t *testing.T) {
	monitor := runQMP(t, func(qmpTestRequest) qmpTestAction { return qmpTestAction{} })
	if err := SystemPowerdown(context.Background(), monitor.socket); err != nil {
		t.Fatal(err)
	}
	if got := <-monitor.received; got.Execute != "system_powerdown" {
		t.Fatalf("command = %q", got.Execute)
	}
}

func usbBindingForTest() usb.Binding {
	return usb.Binding{Name: "board", Bus: 1, Port: "3.2"}
}

func usbDeclForTest() usb.Decl {
	return usb.Decl{Name: "board", Route: usb.Route{Controller: "0000:00:14.0", Protocol: 2, Port: "3.2"}}
}

func requireUSBOutcome(t *testing.T, result USBChangeResult, want USBChangeOutcome) {
	t.Helper()
	if result.Outcome != want {
		t.Fatalf("USB change outcome = %d, want %d; error = %v", result.Outcome, want, result.Err)
	}
	if want == USBChangeApplied && result.Err != nil {
		t.Fatalf("applied USB change returned error: %v", result.Err)
	}
	if want != USBChangeApplied && result.Err == nil {
		t.Fatalf("failed USB change returned no error")
	}

}

func requireUSBFailure(t *testing.T, result USBChangeResult, want USBChangeOutcome) error {
	t.Helper()
	requireUSBOutcome(t, result, want)
	return result.Err
}

func qmpProperties(controller, active bool) []qomProperty {
	properties := []qomProperty{}
	if controller {
		properties = append(properties, qomProperty{Name: "voom-xhci", Type: "child<xHCI>"})
	}
	if active {
		properties = append(properties, qomProperty{Name: "voom-usb-board", Type: "child<usb-host>"})
	}
	return properties
}

type qmpTestEvent struct {
	name string
	data any
}

type qmpTestRequest struct {
	Execute   string         `json:"execute"`
	Arguments map[string]any `json:"arguments,omitempty"`
	ID        uint64         `json:"id"`
}

func deviceDeletedEvent(id string) qmpTestEvent {
	return qmpTestEvent{name: "DEVICE_DELETED", data: map[string]any{"device": id, "path": "/machine/peripheral/" + id}}
}

type qmpTestAction struct {
	result       any
	failure      *qmpFailure
	eventsBefore []qmpTestEvent
	eventsAfter  []qmpTestEvent
	drop         bool
	dropAfter    bool
}

type qmpTestServer struct {
	socket   string
	received <-chan qmpTestRequest
}

func runQMP(t *testing.T, respond func(qmpTestRequest) qmpTestAction) qmpTestServer {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "qemu.mon")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	received := make(chan qmpTestRequest, 16)
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			serveQMPConnection(conn, received, respond)
		}
	}()
	return qmpTestServer{socket: socket, received: received}
}

func serveQMPConnection(conn net.Conn, received chan<- qmpTestRequest, respond func(qmpTestRequest) qmpTestAction) {
	defer func() { _ = conn.Close() }()
	encoder := json.NewEncoder(conn)
	decoder := json.NewDecoder(conn)
	_ = encoder.Encode(map[string]any{"QMP": map[string]any{"version": map[string]any{}, "capabilities": []string{}}})
	for {
		var request qmpTestRequest
		if err := decoder.Decode(&request); err != nil {
			return
		}
		if request.Execute == "qmp_capabilities" {
			_ = encoder.Encode(map[string]any{"return": map[string]any{}, "id": request.ID})
			continue
		}
		received <- request
		action := respond(request)
		if action.drop {
			return
		}
		for _, event := range action.eventsBefore {
			writeQMPTestEvent(encoder, event)
		}
		if action.failure != nil {
			_ = encoder.Encode(map[string]any{"error": action.failure, "id": request.ID})
		} else {
			result := action.result
			if result == nil {
				result = map[string]any{}
			}
			_ = encoder.Encode(map[string]any{"return": result, "id": request.ID})
		}
		for _, event := range action.eventsAfter {
			writeQMPTestEvent(encoder, event)
		}
		if action.dropAfter {
			return
		}
	}
}

func writeQMPTestEvent(encoder *json.Encoder, event qmpTestEvent) {
	_ = encoder.Encode(map[string]any{"event": event.name, "data": event.data})
}
