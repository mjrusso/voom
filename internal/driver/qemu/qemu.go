package qemu

import (
	"fmt"
	"strconv"

	"github.com/mjrusso/voom/internal/pci"
	"github.com/mjrusso/voom/internal/usb"
)

// Share describes one virtio-fs share to attach via virtiofsd (tag + Unix socket path).
type Share struct {
	Tag  string
	Sock string
}

// ArgsConfig is the structured input used to build qemu-system command-line arguments.
type ArgsConfig struct {
	ControlCID   uint32
	ControlPort  uint32
	Arch         string
	FirmwarePath string
	CPUs         int
	MemoryMiB    int
	DiskPath     string
	DiskFormat   string
	SeedImage    string
	NetSock      string
	MAC          string
	SerialLog    string
	MonitorSock  string
	Shares       []Share
	USBDevices   []usb.Binding
	PCIDevices   []pci.Decl
}

// Args returns the qemu-system command-line arguments for the supplied configuration.
func Args(c ArgsConfig) ([]string, error) {
	var args []string
	switch c.Arch {
	case "x86_64-linux":
		args = []string{"-machine", "q35"}
	case "aarch64-linux":
		args = []string{"-machine", "virt", "-bios", c.FirmwarePath}
	default:
		return nil, fmt.Errorf("unsupported QEMU architecture %q", c.Arch)
	}
	args = append(args, "-enable-kvm", "-cpu", "host", "-smp", strconv.Itoa(c.CPUs), "-m", strconv.Itoa(c.MemoryMiB))
	if c.ControlCID != 0 {
		if c.ControlCID < 3 || c.ControlCID == ^uint32(0) || c.ControlPort == 0 || c.ControlPort == ^uint32(0) {
			return nil, fmt.Errorf("invalid vsock CID or control port")
		}
		args = append(args, "-device", fmt.Sprintf("vhost-vsock-pci,guest-cid=%d,vhostfd=3", c.ControlCID), "-fw_cfg", fmt.Sprintf("name=opt/voom/control-port,string=%d", c.ControlPort))
	}
	rootPorts, vfioDevices, err := pciDeviceArguments(c.PCIDevices)
	if err != nil {
		return nil, err
	}
	args = append(args, rootPorts...)
	diskDrive := "file=" + c.DiskPath + ",if=virtio"
	if c.DiskFormat != "" {
		diskDrive += ",format=" + c.DiskFormat
	}
	if hasShare(c.Shares) {
		args = append(args, "-object", "memory-backend-memfd,id=mem,size="+strconv.Itoa(c.MemoryMiB)+"M,share=on")
		args = append(args, "-numa", "node,memdev=mem")
	}
	args = append(args,
		"-drive", diskDrive,
		"-netdev", "stream,id=net0,server=off,addr.type=unix,addr.path="+c.NetSock,
		"-device", "virtio-net-pci,netdev=net0,mac="+c.MAC,
		"-display", "none",
		"-serial", "file:"+c.SerialLog,
		"-qmp", "unix:"+c.MonitorSock+",server=on,wait=off",
	)
	if c.SeedImage != "" {
		args = append(args, "-drive", "file="+c.SeedImage+",if=virtio,format=raw,readonly=on")
	}
	for i, sh := range c.Shares {
		if sh.Tag == "" || sh.Sock == "" {
			continue
		}
		id := "chrfs" + strconv.Itoa(i)
		args = append(args,
			"-chardev", "socket,id="+id+",path="+sh.Sock,
			"-device", "vhost-user-fs-pci,chardev="+id+",tag="+sh.Tag+",queue-size=1024",
		)
	}
	if len(c.USBDevices) > 0 {
		args = append(args, "-device", "qemu-xhci,id=voom-xhci")
	}
	for _, device := range c.USBDevices {
		argument, err := usbDeviceArgument(device)
		if err != nil {
			return nil, err
		}
		args = append(args, "-device", argument)
	}
	return append(args, vfioDevices...), nil
}

// pciDeviceArguments places one root port per assignment, all in guest slot
// 0x10, and the VFIO devices behind them. QEMU assigns automatic addresses in
// command-line order, so the root ports must precede every device without an
// explicit address, or one of those devices can take slot 0x10 first.
func pciDeviceArguments(decls []pci.Decl) (rootPorts, vfioDevices []string, err error) {
	if err := pci.ValidateAssignments(decls); err != nil {
		return nil, nil, err
	}
	for i, decl := range decls {
		port := "voom-rp-" + decl.Name
		rootPort := fmt.Sprintf("pcie-root-port,id=%s,bus=pcie.0,addr=10.%d,chassis=1,slot=%d", port, i, i+1)
		if i == 0 && len(decls) > 1 {
			rootPort += ",multifunction=on"
		}
		rootPorts = append(rootPorts, "-device", rootPort)
		for _, f := range decl.Functions {
			// A single function becomes guest function 0 so the guest enumerates it.
			guestFunction := f.Number
			if len(decl.Functions) == 1 {
				guestFunction = 0
			}
			device := fmt.Sprintf("vfio-pci,host=%s,bus=%s,addr=00.%d,id=voom-pci-%s-%d", decl.Slot.FunctionAddress(f.Number), port, guestFunction, decl.Name, f.Number)
			if guestFunction == 0 && len(decl.Functions) > 1 {
				device += ",multifunction=on"
			}
			vfioDevices = append(vfioDevices, "-device", device)
		}
	}
	return rootPorts, vfioDevices, nil
}

func usbDeviceArgument(device usb.Binding) (string, error) {
	if err := device.Validate(); err != nil {
		return "", err
	}
	return "usb-host,bus=voom-xhci.0,hostbus=" + strconv.Itoa(device.Bus) + ",hostport=" + device.Port + ",id=" + device.QEMUDeviceID(), nil
}

func hasShare(shares []Share) bool {
	for _, sh := range shares {
		if sh.Tag != "" && sh.Sock != "" {
			return true
		}
	}
	return false
}
