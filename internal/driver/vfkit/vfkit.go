package vfkit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mjrusso/voom/internal/host"
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

// LinuxBoot configures direct kernel or EFI boot for a Linux guest.
type LinuxBoot struct {
	KernelPath string
	InitrdPath string
	KernelCmd  string
	EFIStore   string
	SeedImage  string
	SerialLog  string
}

// MacOSBoot identifies the persistent Apple platform files and display title.
type MacOSBoot struct {
	MachineIdentifier string
	HardwareModel     string
	AuxiliaryStorage  string
	WindowTitle       string
}

// ArgsConfig is the structured input used to build vfkit command-line arguments.
type ArgsConfig struct {
	CPUs      int
	MemoryMiB int
	Linux     *LinuxBoot
	MacOS     *MacOSBoot
	BootDisk  string
	NetSock   string
	MAC       string
	RestSock  string
	Shares    []Share
}

// Args returns the vfkit command-line arguments for the supplied configuration.
func Args(c ArgsConfig) ([]string, error) {
	args := []string{
		"--cpus", strconv.Itoa(c.CPUs),
		"--memory", strconv.Itoa(c.MemoryMiB),
		"--device", "virtio-blk,path=" + c.BootDisk,
	}
	if (c.Linux == nil) == (c.MacOS == nil) {
		return nil, fmt.Errorf("vfkit requires exactly one Linux or MacOS boot configuration")
	}
	if boot := c.MacOS; boot != nil {
		if boot.MachineIdentifier == "" || boot.HardwareModel == "" || boot.AuxiliaryStorage == "" {
			return nil, fmt.Errorf("vfkit MacOS boot requires machine identifier, hardware model, and auxiliary storage paths")
		}
		args = append(args,
			"--bootloader", "macos,machineIdentifierPath="+boot.MachineIdentifier+",hardwareModelPath="+boot.HardwareModel+",auxImagePath="+boot.AuxiliaryStorage,
			"--device", "virtio-gpu,width=1280,height=800",
			"--device", "virtio-input,keyboard",
			"--device", "virtio-input,pointing",
			"--gui", "--gui-keep-running", "--gui-title", boot.WindowTitle,
		)
	} else {
		boot := c.Linux
		if boot.KernelPath != "" {
			if boot.EFIStore != "" {
				return nil, fmt.Errorf("vfkit Linux boot cannot combine kernel and EFI configuration")
			}
			args = append(args, "--kernel", boot.KernelPath)
			if boot.InitrdPath != "" {
				args = append(args, "--initrd", boot.InitrdPath)
			}
			if boot.KernelCmd != "" {
				args = append(args, "--kernel-cmdline", boot.KernelCmd)
			}
		} else {
			if boot.EFIStore == "" || boot.InitrdPath != "" || boot.KernelCmd != "" {
				return nil, fmt.Errorf("vfkit Linux EFI boot requires a variable store and no kernel options")
			}
			args = append(args, "--bootloader", "efi,variable-store="+boot.EFIStore+",create")
		}
		args = append(args, "--device", "virtio-serial,logFilePath="+boot.SerialLog)
		if boot.SeedImage != "" {
			args = append(args, "--device", "virtio-blk,path="+boot.SeedImage+",readonly")
		}
	}
	args = append(args,
		"--device", "virtio-net,unixSocketPath="+c.NetSock+",mac="+c.MAC,
		"--device", "virtio-rng",
		"--restful-uri", "unix://"+c.RestSock,
	)
	for _, sh := range c.Shares {
		if sh.MountTag == "" || sh.SharedDir == "" {
			continue
		}
		device := "virtio-fs,sharedDir=" + sh.SharedDir + ",mountTag=" + sh.MountTag
		if sh.Readonly {
			device += ",readonly"
		}
		args = append(args, "--device", device)
	}
	return args, nil
}

// Stop asks vfkit to power off via its REST socket. If hard is true, the VM is force-stopped.
func Stop(restSock string, hard bool) error {
	state := "Stop"
	if hard {
		state = "HardStop"
	}
	return post(context.Background(), restSock, "/vm/state", map[string]string{"state": state})
}

// ShowDisplay focuses the existing window through the running driver's control socket.
func ShowDisplay(ctx context.Context, sock string) error {
	return post(ctx, sock, "/vm/display", map[string]string{"action": "show"})
}

// ResolveCompatible selects vfkit and checks the extensions required by the caller.
func ResolveCompatible(ctx context.Context, required ...string) (string, error) {
	executable, err := host.ExePath("vfkit")
	if err != nil {
		return "", err
	}
	if len(required) == 0 {
		return executable, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, executable, "--capabilities").Output()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		return "", fmt.Errorf("%s: probe vfkit capabilities: %w; install Voom's bundled vfkit or set VOOM_VFKIT to a compatible build", executable, err)
	}
	var capabilities map[string]int
	if err := json.Unmarshal(data, &capabilities); err != nil {
		return "", fmt.Errorf("%s: invalid vfkit capabilities: %w", executable, err)
	}
	for _, capability := range required {
		if capabilities[capability] != 1 {
			return "", fmt.Errorf("%s: vfkit requires %s; install Voom's bundled vfkit or set VOOM_VFKIT to a compatible build", executable, capability)
		}
	}
	return executable, nil
}

func post(ctx context.Context, sock, path string, body map[string]string) error {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix"+path, bytes.NewReader(b))
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
