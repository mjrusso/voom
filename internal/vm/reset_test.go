package vm

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mjrusso/voom/internal/host"
	"github.com/mjrusso/voom/internal/process"
	"github.com/mjrusso/voom/internal/share"
	"github.com/mjrusso/voom/internal/state"
)

func TestLinuxResetRejectsIncompatibleImageBeforeStoppingRuntime(t *testing.T) {
	for _, tc := range []struct {
		name               string
		capabilities       state.ImageCapabilities
		share, autoForward bool
		format             string
		want               string
	}{
		{name: "share without control", capabilities: state.ImageCapabilities{GuestShareMount: true}, share: true, want: "requires controlShare and guestShareMount"},
		{name: "share without mount service", capabilities: state.ImageCapabilities{ControlShare: true}, share: true, want: "requires controlShare and guestShareMount"},
		{name: "forward without control", capabilities: state.ImageCapabilities{GuestPortReport: true}, autoForward: true, want: "capabilit"},
		{name: "forward without reports", capabilities: state.ImageCapabilities{ControlShare: true}, autoForward: true, want: "capabilit"},
		{name: "unsupported disk format", format: "raw", want: "qemu requires qcow2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, _ := newTestStore(t)
			image := &state.ImageRecord{SchemaVersion: 1, ID: "replacement", Name: "replacement", Arch: host.System(), Format: "qcow2", Disk: "disk.qcow2", Capabilities: tc.capabilities}
			if tc.format != "" {
				image.Format = tc.format
			}
			if err := state.WriteJSONAtomic(filepath.Join(st.ImageDir(image.ID), "image.json"), image); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(st.ImageDiskPath(image), []byte("replacement disk"), 0600); err != nil {
				t.Fatal(err)
			}
			idx := st.IndexSnapshot()
			idx.Images[image.Name] = image.ID
			if err := st.SetIndexSnapshot(idx); err != nil {
				t.Fatal(err)
			}
			record := &state.VMRecord{SchemaVersion: 1, ID: "linux", Name: "linux", Arch: host.System(), Driver: "qemu", Image: state.VMImageRef{ID: "original", Name: "original"}, Resources: state.VMResources{CPUs: 2, MemoryMiB: 2048}, Network: state.VMNetwork{AutoForward: tc.autoForward}}
			if tc.share {
				record.Shares = []share.Decl{{Tag: "project", HostPath: t.TempDir(), GuestPath: "/mnt/project"}}
			}
			if err := st.SaveVM(record); err != nil {
				t.Fatal(err)
			}
			if err := st.RegisterVM(record.Name, record.ID); err != nil {
				t.Fatal(err)
			}
			disk := st.VMDiskPath(record)
			if err := os.WriteFile(disk, []byte("guest data"), 0600); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(st.VMDir(record.ID), "vm.json"))
			if err != nil {
				t.Fatal(err)
			}
			driver := fakeNamedVMProcess(t, "qemu-system-test")
			writeProcessRecord(t, st.Runtime(record).VMProcessRecord(), driver)
			gateway := fakeNamedVMProcess(t, "gvproxy")
			writeProcessRecord(t, st.Runtime(record).GVProxyProcessRecord(), gateway)
			if err := New(st).ResetDisk(context.Background(), record.Name, image.Name); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("incompatible reset error = %v; want %q", err, tc.want)
			}
			if pid, running := process.ValidRecord(st.Runtime(record).VMProcessRecord(), "qemu"); !running || pid != driver.Process.Pid {
				t.Fatal("rejected reset stopped or replaced the driver")
			}
			if pid, running := process.ValidRecord(st.Runtime(record).GVProxyProcessRecord(), "gvproxy"); !running || pid != gateway.Process.Pid {
				t.Fatal("rejected reset stopped or replaced the network")
			}
			after, err := os.ReadFile(filepath.Join(st.VMDir(record.ID), "vm.json"))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("rejected reset changed VM record: %v", err)
			}
			if data, err := os.ReadFile(disk); err != nil || string(data) != "guest data" {
				t.Fatalf("rejected reset changed disk: %q, %v", data, err)
			}
		})
	}
}

func TestImageAccessDefaults(t *testing.T) {
	for _, tc := range []struct {
		name     string
		metadata state.ImageMetadata
		macOS    bool
		want     state.VMAccess
	}{
		{name: "Linux root default", want: state.VMAccess{SSHUser: "root", NixosTargetUser: "root"}},
		{name: "Linux login user", metadata: state.ImageMetadata{SSHUser: "debian", SSHIdentityPath: "/keys/debian"}, want: state.VMAccess{SSHUser: "debian", SSHIdentityPath: "/keys/debian", NixosTargetUser: "debian"}},
		{name: "Linux explicit target", metadata: state.ImageMetadata{SSHUser: "debian", NixosTargetUser: "root"}, want: state.VMAccess{SSHUser: "debian", NixosTargetUser: "root"}},
		{name: "macOS has no NixOS target", macOS: true, metadata: state.ImageMetadata{SSHUser: "alice", SSHIdentityPath: "/keys/alice", NixosTargetUser: "root"}, want: state.VMAccess{SSHUser: "alice", SSHIdentityPath: "/keys/alice"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			image := &state.ImageRecord{ID: "new", Name: "new", Metadata: tc.metadata}
			if tc.macOS {
				image.MacOS = &state.MacOSPlatform{}
			}
			for _, original := range []state.VMRecord{{}, {Access: state.VMAccess{SSHUser: "old", SSHIdentityPath: "/keys/old", NixosTargetUser: "old-target"}}} {
				candidate := vmFromImage(original, image)
				if candidate.Access != tc.want {
					t.Fatalf("access = %+v; want %+v", candidate.Access, tc.want)
				}
			}
		})
	}
}
