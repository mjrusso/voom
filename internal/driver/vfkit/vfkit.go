package vfkit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	clientsMu sync.Mutex
	clients   = map[string]*http.Client{}
)

// clientForSocket returns a cached http.Client that dials the given unix socket,
// so repeated calls to the same socket reuse connections.
func clientForSocket(sock string) *http.Client {
	clientsMu.Lock()
	defer clientsMu.Unlock()
	if c, ok := clients[sock]; ok {
		return c
	}
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}}
	c := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	clients[sock] = c
	return c
}

// Share describes one virtio-fs share to attach via vfkit (mount tag + host directory).
type Share struct {
	MountTag  string
	SharedDir string
	Readonly  bool
}

// ArgsConfig is the structured input used to build vfkit command-line arguments.
type ArgsConfig struct {
	CPUs       int
	MemoryMiB  int
	KernelPath string
	InitrdPath string
	KernelCmd  string
	BootDisk   string
	SeedImage  string
	NetSock    string
	MAC        string
	SerialLog  string
	RestSock   string
	EFIStore   string
	Shares     []Share
}

// Args returns the vfkit command-line arguments for the supplied configuration.
func Args(c ArgsConfig) []string {
	args := []string{
		"--cpus", strconv.Itoa(c.CPUs),
		"--memory", strconv.Itoa(c.MemoryMiB),
	}
	if c.KernelPath != "" {
		args = append(args, "--kernel", c.KernelPath)
		if c.InitrdPath != "" {
			args = append(args, "--initrd", c.InitrdPath)
		}
		if c.KernelCmd != "" {
			args = append(args, "--kernel-cmdline", c.KernelCmd)
		}
	} else {
		args = append(args, "--bootloader", "efi,variable-store="+c.EFIStore+",create")
	}
	args = append(args,
		"--device", "virtio-blk,path="+c.BootDisk,
		"--device", "virtio-net,unixSocketPath="+c.NetSock+",mac="+c.MAC,
		"--device", "virtio-serial,logFilePath="+c.SerialLog,
		"--device", "virtio-rng",
		"--restful-uri", "unix://"+c.RestSock,
	)
	if c.SeedImage != "" {
		args = append(args, "--device", "virtio-blk,path="+c.SeedImage+",readonly")
	}
	for _, sh := range c.Shares {
		if sh.MountTag == "" || sh.SharedDir == "" {
			continue
		}
		args = append(args, "--device", "virtio-fs,sharedDir="+sh.SharedDir+",mountTag="+sh.MountTag)
	}
	return args
}

// Stop asks vfkit to power off via its REST socket. If hard is true, the VM is force-stopped.
func Stop(restSock string, hard bool) error {
	state := "Stop"
	if hard {
		state = "HardStop"
	}
	return post(restSock, "/vm/state", map[string]string{"state": state})
}

func post(sock, path string, body any) error {
	// json.Marshal on map[string]string here cannot fail.
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, "http://unix"+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := clientForSocket(sock).Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		rb, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("vfkit %s failed: %s", path, strings.TrimSpace(string(rb)))
	}
	return nil
}
