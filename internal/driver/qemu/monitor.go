package qemu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/mjrusso/voom/internal/usb"
)

const monitorTimeout = 5 * time.Second

// USBChangeOutcome describes what QEMU confirmed about a requested USB runtime state.
type USBChangeOutcome uint8

const (
	// USBChangeNotApplied means no target-device mutation took effect.
	USBChangeNotApplied USBChangeOutcome = iota
	// USBChangeApplied means QEMU confirmed the requested runtime state.
	USBChangeApplied
	// USBChangeUnknown means a mutation was issued but its result could not be observed.
	USBChangeUnknown
)

// USBChangeResult contains a USB runtime outcome and its failure, if any.
type USBChangeResult struct {
	Outcome USBChangeOutcome
	Err     error
}

type qmpClient struct {
	conn    net.Conn
	encoder *json.Encoder
	decoder *json.Decoder
	nextID  uint64
	events  []qmpMessage
}

type qmpMessage struct {
	Greeting json.RawMessage `json:"QMP,omitempty"`
	Return   json.RawMessage `json:"return,omitempty"`
	Failure  *qmpFailure     `json:"error,omitempty"`
	Event    string          `json:"event,omitempty"`
	Data     json.RawMessage `json:"data,omitempty"`
	ID       uint64          `json:"id,omitempty"`
}

type qmpFailure struct {
	Class string `json:"class"`
	Desc  string `json:"desc"`
}

func (e *qmpFailure) Error() string {
	if e.Class == "" {
		return e.Desc
	}
	return e.Class + ": " + e.Desc
}

type qmpRequest[A any] struct {
	Execute   string `json:"execute"`
	Arguments *A     `json:"arguments,omitempty"`
	ID        uint64 `json:"id"`
}

type qmpNoArguments struct{}

type qmpQOMListArguments struct {
	Path string `json:"path"`
}

type qmpDeviceAddArguments struct {
	Driver   string `json:"driver"`
	ID       string `json:"id"`
	Bus      string `json:"bus,omitempty"`
	HostBus  int    `json:"hostbus,omitempty"`
	HostPort string `json:"hostport,omitempty"`
}

type qmpDeviceDeleteArguments struct {
	ID string `json:"id"`
}

type qomProperty struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type qmpDeviceEvent struct {
	Device string `json:"device"`
	Path   string `json:"path"`
}

type qmpDeviceUnplugGuestError struct {
	deviceID string
}

func (e *qmpDeviceUnplugGuestError) Error() string {
	return fmt.Sprintf("QEMU guest rejected removal of USB device %q", e.deviceID)
}

type contextConn struct {
	net.Conn
	stopClose func() bool
}

func (c *contextConn) Close() error {
	c.stopClose()
	return c.Conn.Close()
}

func dialQMP(ctx context.Context, socket string) (*qmpClient, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, fmt.Errorf("connect to QEMU monitor: %w", err)
	}
	deadline := time.Now().Add(monitorTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		_ = conn.Close()
		return nil, err
	}
	stopClose := context.AfterFunc(ctx, func() { _ = conn.Close() })
	client := &qmpClient{conn: conn, encoder: json.NewEncoder(conn), decoder: json.NewDecoder(conn)}
	var greeting qmpMessage
	if err := client.decoder.Decode(&greeting); err != nil {
		stopClose()
		_ = conn.Close()
		return nil, fmt.Errorf("read QMP greeting: %w", err)
	}
	if len(greeting.Greeting) == 0 {
		stopClose()
		_ = conn.Close()
		return nil, errors.New("QEMU monitor did not send a QMP greeting")
	}
	if _, err := qmpCall[qmpNoArguments, struct{}](client, "qmp_capabilities", nil); err != nil {
		stopClose()
		_ = conn.Close()
		return nil, fmt.Errorf("negotiate QMP capabilities: %w", err)
	}
	client.conn = &contextConn{Conn: conn, stopClose: stopClose}
	return client, nil
}

func (c *qmpClient) close() {
	_ = c.conn.Close()
}

func qmpCall[A, R any](c *qmpClient, command string, arguments *A) (R, error) {
	var result R
	c.nextID++
	id := c.nextID
	if err := c.encoder.Encode(qmpRequest[A]{Execute: command, Arguments: arguments, ID: id}); err != nil {
		return result, fmt.Errorf("write QMP command %s: %w", command, err)
	}
	for {
		message, err := c.read()
		if err != nil {
			return result, fmt.Errorf("read QMP response for %s: %w", command, err)
		}
		if message.Event != "" {
			c.events = append(c.events, message)
			continue
		}
		if message.ID != id {
			if message.Failure != nil && message.ID == 0 {
				return result, message.Failure
			}
			continue
		}
		if message.Failure != nil {
			return result, message.Failure
		}
		if message.Return == nil {
			return result, fmt.Errorf("QMP command %s returned no result", command)
		}
		if err := json.Unmarshal(message.Return, &result); err != nil {
			return result, fmt.Errorf("decode QMP response for %s: %w", command, err)
		}
		return result, nil
	}
}

func (c *qmpClient) read() (qmpMessage, error) {
	var message qmpMessage
	if err := c.decoder.Decode(&message); err != nil {
		return qmpMessage{}, err
	}
	return message, nil
}

func (c *qmpClient) waitForDeviceDeleted(deviceID string) error {
	for {
		if len(c.events) > 0 {
			message := c.events[0]
			c.events = c.events[1:]
			if done, err := deviceDeleteEvent(message, deviceID); done {
				return err
			}
			continue
		}
		message, err := c.read()
		if err != nil {
			return err
		}
		if done, err := deviceDeleteEvent(message, deviceID); done {
			return err
		}
	}
}

func deviceDeleteEvent(message qmpMessage, deviceID string) (bool, error) {
	if message.Event != "DEVICE_DELETED" && message.Event != "DEVICE_UNPLUG_GUEST_ERROR" {
		return false, nil
	}
	var event qmpDeviceEvent
	if err := json.Unmarshal(message.Data, &event); err != nil {
		return true, fmt.Errorf("decode QMP %s event: %w", message.Event, err)
	}
	if event.Device != deviceID && !strings.HasSuffix(event.Path, "/"+deviceID) {
		return false, nil
	}
	if message.Event == "DEVICE_UNPLUG_GUEST_ERROR" {
		return true, &qmpDeviceUnplugGuestError{deviceID: deviceID}
	}
	return true, nil
}

func qmpExecute[A, R any](ctx context.Context, socket, command string, arguments *A) (R, error) {
	var result R
	client, err := dialQMP(ctx, socket)
	if err != nil {
		return result, err
	}
	defer client.close()
	return qmpCall[A, R](client, command, arguments)
}

func qmpDeviceIDs(ctx context.Context, socket string) (map[string]bool, error) {
	properties, err := qmpExecute[qmpQOMListArguments, []qomProperty](ctx, socket, "qom-list", &qmpQOMListArguments{Path: "/machine/peripheral"})
	if err != nil {
		return nil, err
	}
	ids := make(map[string]bool, len(properties))
	for _, property := range properties {
		if strings.HasPrefix(property.Type, "child<") {
			ids[property.Name] = true
		}
	}
	return ids, nil
}

func qmpAddDevice(ctx context.Context, socket string, arguments qmpDeviceAddArguments) USBChangeResult {
	client, err := dialQMP(ctx, socket)
	if err != nil {
		return USBChangeResult{Outcome: USBChangeNotApplied, Err: err}
	}
	defer client.close()
	if _, err := qmpCall[qmpDeviceAddArguments, struct{}](client, "device_add", &arguments); err != nil {
		var failure *qmpFailure
		if errors.As(err, &failure) {
			return USBChangeResult{Outcome: USBChangeNotApplied, Err: err}
		}
		return USBChangeResult{Outcome: USBChangeUnknown, Err: err}
	}
	return USBChangeResult{Outcome: USBChangeApplied}
}

func qmpDeleteDevice(ctx context.Context, socket, id string) USBChangeResult {
	client, err := dialQMP(ctx, socket)
	if err != nil {
		return USBChangeResult{Outcome: USBChangeNotApplied, Err: err}
	}
	defer client.close()
	if _, err := qmpCall[qmpDeviceDeleteArguments, struct{}](client, "device_del", &qmpDeviceDeleteArguments{ID: id}); err != nil {
		var failure *qmpFailure
		if errors.As(err, &failure) {
			return USBChangeResult{Outcome: USBChangeNotApplied, Err: err}
		}
		return USBChangeResult{Outcome: USBChangeUnknown, Err: err}
	}
	if err := client.waitForDeviceDeleted(id); err != nil {
		var guestErr *qmpDeviceUnplugGuestError
		if errors.As(err, &guestErr) {
			return USBChangeResult{Outcome: USBChangeNotApplied, Err: err}
		}
		return USBChangeResult{Outcome: USBChangeUnknown, Err: err}
	}
	return USBChangeResult{Outcome: USBChangeApplied}
}

// AttachUSB adds a USB assignment to a running QEMU process.
func AttachUSB(ctx context.Context, socket string, device usb.Binding) USBChangeResult {
	if err := device.Validate(); err != nil {
		return USBChangeResult{Outcome: USBChangeNotApplied, Err: err}
	}
	ids, err := qmpDeviceIDs(ctx, socket)
	if err != nil {
		return USBChangeResult{Outcome: USBChangeNotApplied, Err: err}
	}
	deviceID := device.QEMUDeviceID()
	if ids[deviceID] {
		return USBChangeResult{Outcome: USBChangeApplied}
	}
	if !ids["voom-xhci"] {
		controller := qmpAddDevice(ctx, socket, qmpDeviceAddArguments{Driver: "qemu-xhci", ID: "voom-xhci"})
		if controller.Outcome != USBChangeApplied {
			ids, inspectErr := qmpDeviceIDs(ctx, socket)
			if inspectErr != nil || !ids["voom-xhci"] {
				return USBChangeResult{Outcome: USBChangeNotApplied, Err: fmt.Errorf("add USB controller: %w", errors.Join(controller.Err, inspectErr))}
			}
		}
	}
	arguments := qmpDeviceAddArguments{Driver: "usb-host", ID: deviceID, Bus: "voom-xhci.0", HostBus: device.Bus, HostPort: device.Port}
	change := qmpAddDevice(ctx, socket, arguments)
	if change.Outcome == USBChangeNotApplied {
		return USBChangeResult{Outcome: USBChangeNotApplied, Err: fmt.Errorf("attach USB device %q: %w", device.Name, change.Err)}
	}
	if change.Outcome == USBChangeUnknown {
		active, inspectErr := qmpDeviceIDs(ctx, socket)
		if inspectErr == nil && active[deviceID] {
			return USBChangeResult{Outcome: USBChangeApplied}
		}
		result := fmt.Errorf("attach USB device %q: %w", device.Name, errors.Join(change.Err, inspectErr))
		if inspectErr != nil {
			return USBChangeResult{Outcome: USBChangeUnknown, Err: result}
		}
		return USBChangeResult{Outcome: USBChangeNotApplied, Err: result}
	}
	return change
}

// DetachUSB removes a USB assignment from a running QEMU process.
func DetachUSB(ctx context.Context, socket string, device usb.Decl) USBChangeResult {
	if err := device.Validate(); err != nil {
		return USBChangeResult{Outcome: USBChangeNotApplied, Err: err}
	}
	active, err := ActiveUSBDevices(ctx, socket, []usb.Decl{device})
	if err != nil {
		return USBChangeResult{Outcome: USBChangeNotApplied, Err: err}
	}
	if !active[device.Name] {
		return USBChangeResult{Outcome: USBChangeApplied}
	}
	change := qmpDeleteDevice(ctx, socket, device.QEMUDeviceID())
	if change.Outcome == USBChangeNotApplied {
		return USBChangeResult{Outcome: USBChangeNotApplied, Err: fmt.Errorf("detach USB device %q: %w", device.Name, change.Err)}
	}
	if change.Outcome == USBChangeApplied {
		return change
	}
	active, inspectErr := ActiveUSBDevices(ctx, socket, []usb.Decl{device})
	if inspectErr == nil && !active[device.Name] {
		return USBChangeResult{Outcome: USBChangeApplied}
	}
	return USBChangeResult{Outcome: USBChangeUnknown, Err: fmt.Errorf("detach USB device %q: %w", device.Name, errors.Join(change.Err, inspectErr))}
}

// ActiveUSBDevices reports whether each assignment has a QEMU device object.
func ActiveUSBDevices(ctx context.Context, socket string, devices []usb.Decl) (map[string]bool, error) {
	for _, device := range devices {
		if err := device.Validate(); err != nil {
			return nil, err
		}
	}
	ids, err := qmpDeviceIDs(ctx, socket)
	if err != nil {
		return nil, err
	}
	active := make(map[string]bool, len(devices))
	for _, device := range devices {
		active[device.Name] = ids[device.QEMUDeviceID()]
	}
	return active, nil
}

// SystemPowerdown requests a graceful guest shutdown through QMP.
func SystemPowerdown(ctx context.Context, socket string) error {
	_, err := qmpExecute[qmpNoArguments, struct{}](ctx, socket, "system_powerdown", nil)
	return err
}
