package state

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"
)

// ValidateName ensures name is a non-empty alphanumeric/-/_ identifier not starting with - or _.
func ValidateName(kind, name string) error {
	if ok := strings.TrimSpace(name); ok == "" {
		return fmt.Errorf("invalid %s name %q", kind, name)
	}
	for i, r := range name {
		valid := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-'
		if !valid || i == 0 && (r == '_' || r == '-') {
			return fmt.Errorf("invalid %s name %q", kind, name)
		}
	}
	return nil
}

// ParsePort parses s as a TCP port number in the range 1-65535.
func ParsePort(s string) (int, error) {
	p, err := strconv.Atoi(s)
	if err != nil || p < 1 || p > 65535 {
		return 0, fmt.Errorf("invalid TCP port %q", s)
	}
	return p, nil
}

// ParseMemoryMiB parses a size string (e.g. "2GiB") and returns the value in mebibytes.
func ParseMemoryMiB(s string) (int, error) {
	n, err := ParseSize(s)
	if err != nil {
		return 0, err
	}
	mib := n / (1024 * 1024)
	if mib < 1 || mib > math.MaxInt32 {
		return 0, fmt.Errorf("invalid memory size %q", s)
	}
	return int(mib), nil
}

// ParseSize parses a size string with a required unit suffix (M/MB/MiB/G/GB/GiB/T/TB/TiB) into bytes.
func ParseSize(s string) (int64, error) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return 0, errors.New("size is required")
	}
	i := 0
	for i < len(raw) && raw[i] >= '0' && raw[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	if i == len(raw) {
		return 0, fmt.Errorf("bare size %q is not accepted; use MiB, GiB, or similar suffix", s)
	}
	n, err := strconv.ParseInt(raw[:i], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	unit := strings.ToLower(raw[i:])
	mult := map[string]int64{"m": 1024 * 1024, "mb": 1000 * 1000, "mib": 1024 * 1024, "g": 1024 * 1024 * 1024, "gb": 1000 * 1000 * 1000, "gib": 1024 * 1024 * 1024, "t": 1024 * 1024 * 1024 * 1024, "tb": 1000 * 1000 * 1000 * 1000, "tib": 1024 * 1024 * 1024 * 1024}
	m, ok := mult[unit]
	if !ok {
		return 0, fmt.Errorf("invalid size suffix %q", raw[i:])
	}
	if n > math.MaxInt64/m {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	return n * m, nil
}

// InferFormat returns the disk format ("qcow2" or "raw") inferred from path's extension, or "" if unknown.
func InferFormat(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".qcow2", ".qcow":
		return "qcow2"
	case ".raw", ".img":
		return "raw"
	default:
		return ""
	}
}

// ImageFormatFromDisk returns the disk format inferred from the file extension at path.
func ImageFormatFromDisk(path string) string {
	return InferFormat(path)
}

// DiskExtension returns the VM disk file extension for the given driver ("img" for vfkit, "qcow2" otherwise).
func DiskExtension(driver string) string {
	if driver == "vfkit" {
		return "img"
	}
	return "qcow2"
}

// DiskFormat returns the disk format used by the given driver ("raw" for vfkit, "qcow2" otherwise).
func DiskFormat(driver string) string {
	if driver == "vfkit" {
		return "raw"
	}
	return "qcow2"
}

// ParseKernelCmdline extracts the kernel command line from an image metadata map,
// checking common keys and falling back to the org.nixos.bootspec.v1 entry.
func ParseKernelCmdline(raw map[string]any) []string {
	for _, key := range []string{"kernelCmdline", "kernel_cmdline", "vfkitKernelCmdline", "vfkit_kernel_cmdline"} {
		v, ok := raw[key]
		if !ok {
			continue
		}
		switch vv := v.(type) {
		case string:
			if strings.TrimSpace(vv) == "" {
				return nil
			}
			return strings.Fields(vv)
		case []any:
			out := make([]string, 0, len(vv))
			for _, entry := range vv {
				if s, ok := entry.(string); ok && s != "" {
					out = append(out, s)
				}
			}
			return out
		}
	}
	if spec := bootspecMap(raw); spec != nil {
		if params := parseKernelCmdlineValue(spec["kernelParams"]); len(params) > 0 {
			return params
		}
	}
	if params := parseKernelCmdlineValue(raw["kernelParams"]); len(params) > 0 {
		return params
	}
	return nil
}

func parseKernelCmdlineValue(v any) []string {
	switch vv := v.(type) {
	case string:
		if strings.TrimSpace(vv) == "" {
			return nil
		}
		return strings.Fields(vv)
	case []any:
		out := make([]string, 0, len(vv))
		for _, entry := range vv {
			if s, ok := entry.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
