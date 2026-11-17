package vm

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/mjrusso/voom/internal/forward"
	"github.com/mjrusso/voom/internal/process"
)

func TestForwardRowsRetainsOneJournalSnapshot(t *testing.T) {
	st, m, vm, _ := newAutoForwardFixture(t)
	path := st.Runtime(vm).ForwardMutation()
	fingerprint, err := process.RecordFingerprint(st.Runtime(vm).GVProxyProcessRecord())
	if err != nil {
		t.Fatal(err)
	}
	endpoint := forward.Decl{Protocol: "tcp", Bind: "127.0.0.1", HostPort: 18080, GuestPort: 8080}
	journal := forward.MutationJournal{Version: 1, Process: fingerprint, Requests: []forward.MutationRequest{{ID: "1", Kind: forward.ManualExpose, Endpoint: endpoint, GuestTargetIP: m.GuestTargetIP(vm), Result: forward.UnknownMutation(), Error: "response lost"}}}
	data, err := json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	written := make(chan error, 1)
	go func() {
		var fd int
		err := process.WaitForContext(ctx, func() bool {
			var openErr error
			fd, openErr = unix.Open(path, unix.O_WRONLY|unix.O_NONBLOCK, 0)
			return openErr == nil
		}, 5*time.Second)
		if err != nil {
			written <- err
			return
		}
		writer := os.NewFile(uintptr(fd), path)
		_, writeErr := writer.Write(data)
		removeErr := os.Remove(path)
		written <- errors.Join(writeErr, removeErr, writer.Close())
	}()
	rows, err := m.ForwardRows(ctx, []string{vm.Name})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("journal snapshot lost its reservation: %+v", rows)
	}
	if rows[1].Kind != "reservation" || rows[1].HostPort != endpoint.HostPort {
		t.Fatalf("wrong reservation: %+v", rows[1])
	}
	for _, row := range rows {
		if row.Status != "uncertain" || !strings.Contains(row.Reason, "response lost") || strings.Contains(row.Reason, "stop and start") {
			t.Fatalf("inconsistent journal snapshot: %+v", row)
		}
	}
	rows, err = m.ForwardRows(ctx, []string{vm.Name})
	if err != nil || len(rows) != 1 || rows[0].Status != "running" || rows[0].Reason != "" {
		t.Fatalf("removed journal still requires recovery: %+v, %v", rows, err)
	}
}
