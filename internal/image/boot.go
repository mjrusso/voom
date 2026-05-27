package image

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	diskfs "github.com/diskfs/go-diskfs"
	"github.com/diskfs/go-diskfs/partition/gpt"
)

const sectorSize = 512

// DirectBoot describes the kernel, initrd, init path, and kernel cmdline that vfkit needs for direct boot.
type DirectBoot struct {
	KernelRelPath string
	InitrdRelPath string
	InitPath      string
	KernelCmdline []string
}

// ValidateVFKitBootDisk rejects raw disks that are obviously incompatible with
// the current EFI-based vfkit boot path.
func ValidateVFKitBootDisk(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	buf := make([]byte, sectorSize*2)
	n, err := f.Read(buf)
	if err != nil {
		return err
	}
	if n < sectorSize {
		return errors.New("raw disk is too small to inspect boot records")
	}
	if string(buf[sectorSize:sectorSize+8]) == "EFI PART" {
		return nil
	}
	if buf[510] != 0x55 || buf[511] != 0xaa {
		return errors.New("raw disk does not contain a recognizable MBR or GPT header for vfkit EFI boot")
	}
	for i := 0; i < 4; i++ {
		ptype := buf[446+i*16+4]
		if ptype == 0xef || ptype == 0xee {
			return nil
		}
	}
	return fmt.Errorf("raw disk %s is not EFI-bootable for vfkit: missing GPT/ESP layout; rebuild or convert the image with EFI support, or import metadata that provides kernel/initrd/init paths for vfkit direct boot", path)
}

// ExtractVFKitDirectBoot mounts the raw disk's ESP, parses the NixOS GRUB config, and copies kernel/initrd into imageDir for vfkit direct boot.
func ExtractVFKitDirectBoot(rawDiskPath, imageDir string) (*DirectBoot, error) {
	disk, err := diskfs.Open(rawDiskPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = disk.Close() }()

	tableRaw, err := disk.GetPartitionTable()
	if err != nil {
		return nil, err
	}
	table, ok := tableRaw.(*gpt.Table)
	if !ok {
		return nil, errors.New("raw disk does not contain a GPT partition table")
	}

	efiIndex := 0
	for _, part := range table.Partitions {
		if part != nil && part.Type == gpt.EFISystemPartition {
			efiIndex = part.Index
			break
		}
	}
	if efiIndex == 0 {
		return nil, errors.New("raw disk does not contain an EFI system partition")
	}

	fs, err := disk.GetFilesystem(efiIndex)
	if err != nil {
		return nil, err
	}
	defer func() { _ = fs.Close() }()

	grubCfg, err := fs.ReadFile("/grub/grub.cfg")
	if err != nil {
		return nil, fmt.Errorf("could not read ESP grub config: %w", err)
	}
	kernelPath, initrdPath, params, err := parseGRUBConfig(string(grubCfg))
	if err != nil {
		return nil, err
	}
	kernelBytes, err := fs.ReadFile(kernelPath)
	if err != nil {
		return nil, fmt.Errorf("could not read kernel %s from ESP: %w", kernelPath, err)
	}
	initrdBytes, err := fs.ReadFile(initrdPath)
	if err != nil {
		return nil, fmt.Errorf("could not read initrd %s from ESP: %w", initrdPath, err)
	}

	vfkitDir := filepath.Join(imageDir, "vfkit")
	if err := os.MkdirAll(vfkitDir, 0o755); err != nil {
		return nil, err
	}
	kernelRel := filepath.Join("vfkit", filepath.Base(kernelPath))
	initrdRel := filepath.Join("vfkit", filepath.Base(initrdPath))
	if err := os.WriteFile(filepath.Join(imageDir, kernelRel), kernelBytes, 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(imageDir, initrdRel), initrdBytes, 0o644); err != nil {
		return nil, err
	}

	out := &DirectBoot{
		KernelRelPath: kernelRel,
		InitrdRelPath: initrdRel,
		InitPath:      kernelArgValue(params, "init="),
		KernelCmdline: params,
	}
	return out, nil
}

func parseGRUBConfig(cfg string) (kernelPath, initrdPath string, params []string, err error) {
	for _, line := range strings.Split(cfg, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "linux ") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				return "", "", nil, errors.New("grub linux entry is malformed")
			}
			kernelPath = normalizeGRUBPath(fields[1])
			params = append([]string{}, fields[2:]...)
		}
		if strings.HasPrefix(line, "initrd ") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				return "", "", nil, errors.New("grub initrd entry is malformed")
			}
			initrdPath = normalizeGRUBPath(fields[1])
		}
		if kernelPath != "" && initrdPath != "" {
			return kernelPath, initrdPath, params, nil
		}
	}
	return "", "", nil, errors.New("could not find linux/initrd entries in ESP grub config")
}

func normalizeGRUBPath(raw string) string {
	raw = strings.TrimSpace(raw)
	if idx := strings.Index(raw, ")"); strings.HasPrefix(raw, "(") && idx >= 0 {
		raw = raw[idx+1:]
	}
	raw = strings.TrimLeft(raw, "/")
	raw = strings.TrimLeft(raw, "/")
	return "/" + raw
}

func kernelArgValue(args []string, prefix string) string {
	for _, arg := range args {
		if strings.HasPrefix(arg, prefix) {
			return strings.TrimPrefix(arg, prefix)
		}
	}
	return ""
}
