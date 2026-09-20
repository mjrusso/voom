package qemu

import (
	"strconv"

	"github.com/mjrusso/voom/internal/usb"
)

// Share describes one virtio-fs share to attach via virtiofsd (tag + Unix socket path).
type Share struct {
	Tag  string
	Sock string
}

// ArgsConfig is the structured input used to build qemu-system command-line arguments.
type ArgsConfig struct {
	CPUs        int
	MemoryMiB   int
	DiskPath    string
	DiskFormat  string
	SeedImage   string
	NetSock     string
	MAC         string
	SerialLog   string
	MonitorSock string
	Shares      []Share
	USBDevices  []usb.Binding
}

// Args returns the qemu-system command-line arguments for the supplied configuration.
func Args(c ArgsConfig) ([]string, error) {
	args := []string{"-enable-kvm", "-cpu", "host", "-smp", strconv.Itoa(c.CPUs), "-m", strconv.Itoa(c.MemoryMiB)}
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
	return args, nil
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
