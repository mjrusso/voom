package control

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

const version = 1
const port = 49152

// MaxMessage includes the newline delimiter and bounds allocation per frame.
const MaxMessage = 8 * 1024 * 1024

type message struct {
	Version int             `json:"version"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Read rejects truncated frames and unsupported protocol versions or message types.
func Read(r *bufio.Reader, kind string) (json.RawMessage, error) {
	line, err := r.ReadSlice('\n')
	if err != nil {
		return nil, err
	}
	if len(line) > MaxMessage {
		return nil, fmt.Errorf("control message exceeds %d bytes", MaxMessage)
	}
	var m message
	if err := json.Unmarshal(line, &m); err != nil {
		return nil, err
	}
	if m.Version != version || m.Type != kind {
		return nil, fmt.Errorf("unsupported control message: version=%d type=%q (want %q)", m.Version, m.Type, kind)
	}
	return m.Payload, nil
}

// Write emits one bounded JSON frame followed by a newline.
func Write(w io.Writer, kind string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	data, err = json.Marshal(message{Version: version, Type: kind, Payload: data})
	if err != nil {
		return err
	}
	if len(data)+1 > MaxMessage {
		return fmt.Errorf("control message exceeds %d bytes", MaxMessage)
	}
	_, err = io.Copy(w, bytes.NewReader(append(data, '\n')))
	return err
}
