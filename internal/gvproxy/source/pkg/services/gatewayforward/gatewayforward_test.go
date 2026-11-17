//go:build ignore

// Modified by the Voom project; see https://github.com/mjrusso/voom.
package gatewayforward

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// shortTempDir avoids the MacOS 104-char Unix-socket path limit that t.TempDir()
// can exceed when test names are long.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "gwf")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestRelayHalfCloseAndRemoval(t *testing.T) {
	socket := filepath.Join(shortTempDir(t), "backend.sock")
	backend, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	accepted := make(chan net.Conn, 2)
	go func() {
		for {
			c, err := backend.Accept()
			if err != nil {
				return
			}
			accepted <- c
		}
	}()
	var address string
	f := New(func(_, _ string) (net.Listener, error) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err == nil {
			address = ln.Addr().String()
		}
		return ln, err
	})
	defer f.Close()
	route := Route{Local: "192.168.127.1:3128", Target: socket}
	if err := f.Expose(route); err != nil {
		t.Fatal(err)
	}
	if err := f.Expose(route); err != nil {
		t.Fatal(err)
	}
	if err := f.Expose(Route{Local: route.Local, Target: socket + "other"}); err == nil {
		t.Fatal("accepted conflicting target")
	}
	guest, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer guest.Close()
	var upstream net.Conn
	select {
	case upstream = <-accepted:
	case <-time.After(3 * time.Second):
		t.Fatal("no backend connection")
	}
	defer upstream.Close()
	guest.SetDeadline(time.Now().Add(3 * time.Second))
	upstream.SetDeadline(time.Now().Add(3 * time.Second))
	guest.Write([]byte("request"))
	guest.(*net.TCPConn).CloseWrite()
	data, err := io.ReadAll(upstream)
	if err != nil || string(data) != "request" {
		t.Fatalf("request %q: %v", data, err)
	}
	upstream.Write([]byte("response"))
	upstream.(*net.UnixConn).CloseWrite()
	data, err = io.ReadAll(guest)
	if err != nil || string(data) != "response" {
		t.Fatalf("response %q: %v", data, err)
	}
	guest2, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer guest2.Close()
	select {
	case upstream = <-accepted:
	case <-time.After(3 * time.Second):
		t.Fatal("no second backend connection")
	}
	defer upstream.Close()
	done := make(chan struct{})
	go func() { f.Unexpose(route.Local); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("removal stalled")
	}
	guest2.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := guest2.Read(make([]byte, 1)); err == nil {
		t.Fatal("guest relay survived")
	} else if n, ok := err.(net.Error); ok && n.Timeout() {
		t.Fatal("guest relay still open")
	}
	if len(f.All()) != 0 || !f.Reserved(route.Local) {
		t.Fatal("route not removed or reservation lost")
	}
	if conn, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", address); err == nil {
		conn.Close()
		t.Fatal("listener survived")
	}
	f.Unexpose(route.Local)
}

func TestRemovalDuringConnectionEstablishment(t *testing.T) {
	socket := filepath.Join(shortTempDir(t), "backend.sock")
	backend, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	var address string
	f := New(func(_, _ string) (net.Listener, error) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err == nil {
			address = ln.Addr().String()
		}
		return ln, err
	})
	defer f.Close()
	route := Route{Local: "192.168.127.1:3128", Target: socket}
	if err := f.Expose(route); err != nil {
		t.Fatal(err)
	}
	var clients []net.Conn
	for i := 0; i < 32; i++ {
		conn, err := net.DialTimeout("tcp", address, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		clients = append(clients, conn)
		defer conn.Close()
	}
	done := make(chan struct{})
	go func() { f.Unexpose(route.Local); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("removal did not cancel connection establishment")
	}
	for _, conn := range clients {
		conn.SetReadDeadline(time.Now().Add(time.Second))
		_, err := conn.Read(make([]byte, 1))
		if err == nil {
			t.Fatal("connection survived")
		}
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			t.Fatal("pending connection survived removal")
		}
	}
	if len(f.All()) != 0 {
		t.Fatal("route survived removal")
	}
}

type delayedCloseListener struct {
	net.Listener
	closing chan struct{}
	release chan struct{}
}

func (l *delayedCloseListener) Close() error {
	close(l.closing)
	<-l.release
	return l.Listener.Close()
}

func TestReservationLookupDoesNotWaitForShutdown(t *testing.T) {
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener := &delayedCloseListener{tcp, make(chan struct{}), make(chan struct{})}
	release := sync.OnceFunc(func() { close(listener.release) })
	f := New(func(_, _ string) (net.Listener, error) { return listener, nil })
	t.Cleanup(func() { release(); f.Close() })
	route := Route{Local: "192.168.127.1:3128", Target: "/tmp/backend.sock"}
	if err := f.Expose(route); err != nil {
		t.Fatal(err)
	}
	stopped := make(chan struct{})
	go func() { f.Unexpose(route.Local); close(stopped) }()
	select {
	case <-listener.closing:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not start")
	}
	observed := make(chan bool, 1)
	go func() { observed <- f.Reserved(route.Local) && !f.Reserved("1.1.1.1:443") }()
	select {
	case ok := <-observed:
		if !ok {
			t.Fatal("incorrect reservations during shutdown")
		}
	case <-time.After(time.Second):
		t.Fatal("unrelated connections wait for shutdown")
	}
	release()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not finish")
	}
}
