package image

import (
	"bytes"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"
	"testing"

	diskfs "github.com/diskfs/go-diskfs"
	"github.com/diskfs/go-diskfs/disk"
	"github.com/diskfs/go-diskfs/filesystem"
	"github.com/diskfs/go-diskfs/partition/gpt"
)

func TestValidateVFKitBootDiskAcceptsGPT(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disk.raw")
	buf := make([]byte, sectorSize*2)
	copy(buf[sectorSize:], []byte("EFI PART"))
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateVFKitBootDisk(path); err != nil {
		t.Fatalf("expected GPT disk to validate, got %v", err)
	}
}

func TestValidateVFKitBootDiskRejectsLegacyMBROnlyDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disk.raw")
	buf := make([]byte, sectorSize*2)
	buf[446+4] = 0x83
	buf[510] = 0x55
	buf[511] = 0xaa
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	err := ValidateVFKitBootDisk(path)
	if err == nil || !strings.Contains(err.Error(), "not EFI-bootable for vfkit") {
		t.Fatalf("expected EFI boot rejection, got %v", err)
	}
	if !strings.Contains(err.Error(), "kernel/initrd/init paths") {
		t.Fatalf("expected direct-boot remediation in error, got %v", err)
	}
}

func TestExtractVFKitDirectBootFromEFIImage(t *testing.T) {
	dir := t.TempDir()
	imagePath := filepath.Join(dir, "disk.raw")
	const diskSize = 64 * 1024 * 1024

	diskImage, err := diskfs.Create(imagePath, diskSize, diskfs.SectorSizeDefault)
	if err != nil {
		t.Fatal(err)
	}
	table := &gpt.Table{
		LogicalSectorSize:  512,
		PhysicalSectorSize: 512,
		ProtectiveMBR:      true,
		Partitions: []*gpt.Partition{
			{
				Index: 1,
				Start: 2048,
				Size:  32 * 1024 * 1024,
				Type:  gpt.EFISystemPartition,
				Name:  "ESP",
			},
		},
	}
	if err := diskImage.Partition(table); err != nil {
		t.Fatal(err)
	}
	fs, err := diskImage.CreateFilesystem(disk.FilesystemSpec{
		Partition:   1,
		FSType:      filesystem.TypeFat32,
		VolumeLabel: "ESP",
	})
	if err != nil {
		t.Fatal(err)
	}
	writeFSFile(t, fs, "/grub/grub.cfg", []byte(`menuentry "NixOS" {
  linux ($drive1)//kernels/kernel-Image init=/nix/store/system/init console=hvc0 loglevel=4
  initrd ($drive1)//kernels/initrd
}
`))
	kernelPayload := bytes.Repeat([]byte("kernel"), 1024)
	initrdPayload := bytes.Repeat([]byte("initrd"), 1024)
	writeFSFile(t, fs, "/kernels/kernel-Image", kernelPayload)
	writeFSFile(t, fs, "/kernels/initrd", initrdPayload)
	if err := fs.Close(); err != nil {
		t.Fatal(err)
	}
	if err := diskImage.Close(); err != nil {
		t.Fatal(err)
	}

	boot, err := ExtractVFKitDirectBoot(imagePath, dir)
	if err != nil {
		t.Fatal(err)
	}
	if boot.InitPath != "/nix/store/system/init" {
		t.Fatalf("InitPath = %q", boot.InitPath)
	}
	if got := strings.Join(boot.KernelCmdline, " "); got != "init=/nix/store/system/init console=hvc0 loglevel=4" {
		t.Fatalf("KernelCmdline = %q", got)
	}
	gotKernel, err := os.ReadFile(filepath.Join(dir, boot.KernelRelPath))
	if err != nil {
		t.Fatal(err)
	}
	gotInitrd, err := os.ReadFile(filepath.Join(dir, boot.InitrdRelPath))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotKernel, kernelPayload) {
		t.Fatal("extracted kernel payload mismatch")
	}
	if !bytes.Equal(gotInitrd, initrdPayload) {
		t.Fatal("extracted initrd payload mismatch")
	}
}

func writeFSFile(t *testing.T, fs filesystem.FileSystem, path string, data []byte) {
	t.Helper()
	mkdirAllFS(t, fs, pathpkg.Dir(path))
	f, err := fs.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func mkdirAllFS(t *testing.T, fs filesystem.FileSystem, dir string) {
	t.Helper()
	if dir == "." || dir == "/" || dir == "" {
		return
	}
	current := ""
	for _, part := range strings.Split(strings.TrimPrefix(dir, "/"), "/") {
		if part == "" {
			continue
		}
		current += "/" + part
		if err := fs.Mkdir(current); err != nil && !strings.Contains(err.Error(), "exists") {
			t.Fatal(err)
		}
	}
}
