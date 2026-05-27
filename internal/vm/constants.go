package vm

import "time"

const (
	// gvproxyStartTimeout bounds how long Start waits for gvproxy to come up
	// (pidfile written and control + driver sockets present).
	gvproxyStartTimeout = 5 * time.Second
	// driverStartTimeout bounds how long Start waits for the qemu or vfkit
	// driver process to become reachable.
	driverStartTimeout = 8 * time.Second
	// driverStopTimeout bounds the wait for a driver to exit after a graceful
	// power-down request before falling through to SIGTERM/SIGKILL.
	driverStopTimeout = 8 * time.Second
	// virtiofsdStartTimeout bounds how long startVirtiofsd waits for the
	// virtiofsd vhost-user socket to appear.
	virtiofsdStartTimeout = 5 * time.Second
	// virtiofsdShutdownTimeout is the graceful window before virtiofsd is SIGKILLed.
	virtiofsdShutdownTimeout = 2 * time.Second
	// processStopTimeout is the graceful window before SIGKILLing the VM
	// driver, gvproxy, and the auto-forward watcher.
	processStopTimeout = 3 * time.Second
	// autoForwardTick is the polling interval of WatchAutoForwards.
	autoForwardTick = 2 * time.Second
)

const (
	// autoForwardPortLow and autoForwardPortHigh bound the host port range
	// allocateAutoPort searches when picking a free local port for a guest listener.
	autoForwardPortLow  = 30000
	autoForwardPortHigh = 60000
)
