package control

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestFramingAndRejection(t *testing.T) {
	var wire bytes.Buffer
	if err := Write(&wire, "config", map[string]string{"text": "line\nnext"}); err != nil {
		t.Fatal(err)
	}
	if err := Write(&wire, "report", nil); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReaderSize(&wire, MaxMessage)
	payload, err := Read(reader, "config")
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]string
	if err := json.Unmarshal(payload, &data); err != nil || data["text"] != "line\nnext" {
		t.Fatalf("%s: %v", payload, err)
	}
	if _, err := Read(reader, "report"); err != nil {
		t.Fatal(err)
	}
	for _, wire := range []string{
		`{"version":2,"type":"report"}` + "\n",
		`{"version":1,"type":"wrong"}` + "\n",
		"{\n", strings.Repeat("x", MaxMessage) + "\n", `{"version":1,"type":"report"}`,
	} {
		if _, err := Read(bufio.NewReaderSize(strings.NewReader(wire), MaxMessage), "report"); err == nil {
			t.Fatalf("accepted invalid message of length %d", len(wire))
		}
	}
	if err := Write(&wire, "config", strings.Repeat("x", MaxMessage)); err == nil {
		t.Fatal("accepted oversized outgoing message")
	}
}
