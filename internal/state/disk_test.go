package state

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadDiskUsage(t *testing.T) {
	dir := t.TempDir()

	qcow := filepath.Join(dir, "disk.qcow2")
	header := make([]byte, 32)
	copy(header, "QFI\xfb")
	binary.BigEndian.PutUint64(header[24:], 20<<30)
	if err := os.WriteFile(qcow, header, 0o644); err != nil {
		t.Fatal(err)
	}
	usage, err := ReadDiskUsage(qcow)
	if err != nil || usage.VirtualBytes != 20<<30 {
		t.Fatalf("qcow2 usage = %#v, %v; want 20GiB virtual", usage, err)
	}

	raw := filepath.Join(dir, "disk.img")
	f, err := os.Create(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(1 << 30); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	usage, err = ReadDiskUsage(raw)
	if err != nil || usage.VirtualBytes != 1<<30 || usage.AllocatedBytes >= usage.VirtualBytes {
		t.Fatalf("sparse raw usage = %#v, %v; want 1GiB virtual with fewer bytes allocated", usage, err)
	}

	bogus := filepath.Join(dir, "bogus.qcow2")
	if err := os.WriteFile(bogus, []byte(strings.Repeat("x", 32)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDiskUsage(bogus); err == nil || !strings.Contains(err.Error(), "not a qcow2 image") {
		t.Fatalf("expected qcow2 magic rejection, got %v", err)
	}
}
