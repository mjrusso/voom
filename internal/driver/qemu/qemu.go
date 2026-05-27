package qemu

import "strconv"

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
	Pidfile     string
	Shares      []Share
}

// Args returns the qemu-system command-line arguments for the supplied configuration.
func Args(c ArgsConfig) []string {
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
		"-monitor", "unix:"+c.MonitorSock+",server,nowait",
		"-pidfile", c.Pidfile,
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
	return args
}

func hasShare(shares []Share) bool {
	for _, sh := range shares {
		if sh.Tag != "" && sh.Sock != "" {
			return true
		}
	}
	return false
}
