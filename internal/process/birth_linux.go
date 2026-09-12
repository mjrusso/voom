//go:build linux

package process

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func birth(pid int) (string, error) {
	bootID, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	boot := strings.TrimSpace(string(bootID))
	if boot == "" {
		return "", errors.New("kernel boot identity unavailable")
	}
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return "", err
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return "", errors.New("invalid process stat")
	}
	fields := strings.Fields(string(data)[end+1:])
	if len(fields) < 20 {
		return "", errors.New("invalid process stat")
	}
	if fields[0] == "Z" {
		return "", os.ErrNotExist
	}
	return boot + ":" + fields[19], nil
}
