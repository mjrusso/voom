//go:build ignore

// Modified by the Voom project; see https://github.com/mjrusso/voom.
package gatewayforward

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	Capability                = "gateway-forward-v1"
	IsolationCapability       = "guest-isolation-v1"
	UDPForwardCapability      = "udp-forward-v1"
	BlockedSubnetsCapability  = "blocked-subnets-v1"
	ForwardRecoveryCapability = "forward-recovery-v3"
)

// Capabilities returns the versions supported by this build.
func Capabilities() map[string]int {
	return map[string]int{Capability: 1, IsolationCapability: 1, UDPForwardCapability: 1, BlockedSubnetsCapability: 1, ForwardRecoveryCapability: 1}
}

type Route struct {
	Local             string `json:"local"`
	Target            string `json:"target"`
	ActiveConnections int    `json:"activeConnections"`
}

type relay struct{ guest, backend net.Conn }
type listener struct {
	route   Route
	ln      net.Listener
	cancel  context.CancelFunc
	mu      sync.Mutex
	closing bool
	relays  map[*relay]struct{}
	workers sync.WaitGroup
}

type Forwarder struct {
	mu       sync.Mutex
	listen   func(string, string) (net.Listener, error)
	routes   map[string]*listener
	reserved sync.Map
	closed   bool
}

func New(listen func(string, string) (net.Listener, error)) *Forwarder {
	return &Forwarder{listen: listen, routes: map[string]*listener{}}
}

func (f *Forwarder) Reserved(local string) bool {
	_, reserved := f.reserved.Load(local)
	return reserved
}

func (f *Forwarder) Expose(route Route) error {
	host, port, err := net.SplitHostPort(route.Local)
	if err != nil || net.ParseIP(host).To4() == nil || port == "0" {
		return errors.New("local must be an IPv4 address and nonzero TCP port")
	}
	if !filepath.IsAbs(route.Target) || strings.ContainsRune(route.Target, 0) {
		return errors.New("target must be an absolute Unix socket path")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return errors.New("forwarder is closed")
	}
	if old := f.routes[route.Local]; old != nil {
		if old.route.Target != route.Target {
			return errors.New("listener target conflict")
		}
		return nil
	}
	ln, err := f.listen("tcp", route.Local)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	l := &listener{route: route, ln: ln, cancel: cancel, relays: map[*relay]struct{}{}}
	f.reserved.Store(route.Local, struct{}{})
	f.routes[route.Local] = l
	l.workers.Add(1)
	go l.accept(ctx)
	return nil
}

func (l *listener) accept(ctx context.Context) {
	defer l.workers.Done()
	for {
		guest, err := l.ln.Accept()
		if err != nil {
			return
		}
		l.mu.Lock()
		if l.closing {
			l.mu.Unlock()
			guest.Close()
			return
		}
		r := &relay{guest: guest}
		l.relays[r] = struct{}{}
		l.workers.Add(1)
		l.mu.Unlock()
		go l.run(ctx, r)
	}
}

func (l *listener) run(ctx context.Context, r *relay) {
	defer l.workers.Done()
	defer func() {
		l.mu.Lock()
		delete(l.relays, r)
		r.guest.Close()
		if r.backend != nil {
			r.backend.Close()
		}
		l.mu.Unlock()
	}()
	backend, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", l.route.Target)
	if err != nil {
		return
	}
	l.mu.Lock()
	if l.closing {
		l.mu.Unlock()
		backend.Close()
		return
	}
	r.backend = backend
	l.mu.Unlock()
	done := make(chan struct{})
	go func() { copyHalf(backend, r.guest); close(done) }()
	copyHalf(r.guest, backend)
	<-done
}

func copyHalf(dst, src net.Conn) {
	_, err := io.Copy(dst, src)
	if err != nil {
		dst.Close()
		src.Close()
		return
	}
	if c, ok := dst.(interface{ CloseWrite() error }); ok {
		_ = c.CloseWrite()
	} else {
		dst.Close()
	}
}

func (l *listener) close() {
	l.mu.Lock()
	l.closing = true
	l.cancel()
	l.ln.Close()
	for r := range l.relays {
		r.guest.Close()
		if r.backend != nil {
			r.backend.Close()
		}
	}
	l.mu.Unlock()
	// The accept worker keeps the count nonzero until relay registration has stopped.
	l.workers.Wait()
}

func (f *Forwarder) Unexpose(local string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if l := f.routes[local]; l != nil {
		l.close()
		delete(f.routes, local)
	}
}

func (f *Forwarder) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	for local, l := range f.routes {
		l.close()
		delete(f.routes, local)
	}
}

func (f *Forwarder) All() []Route {
	f.mu.Lock()
	defer f.mu.Unlock()
	routes := []Route{}
	for _, l := range f.routes {
		l.mu.Lock()
		r := l.route
		r.ActiveConnections = 0
		for conn := range l.relays {
			if conn.backend != nil {
				r.ActiveConnections++
			}
		}
		l.mu.Unlock()
		routes = append(routes, r)
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].Local < routes[j].Local })
	return routes
}

func (f *Forwarder) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /services/gateway-forward/capabilities", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(Capabilities())
	})
	mux.HandleFunc("GET /services/gateway-forward/all", func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(f.All()) })
	for _, action := range []string{"expose", "unexpose"} {
		mux.HandleFunc("POST /services/gateway-forward/"+action, func(w http.ResponseWriter, r *http.Request) {
			var route Route
			dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&route); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			if err := dec.Decode(&struct{}{}); err != io.EOF {
				http.Error(w, "unexpected trailing data", 400)
				return
			}
			if err := r.Context().Err(); err != nil {
				http.Error(w, err.Error(), 408)
				return
			}
			if action == "expose" {
				if err := f.Expose(route); err != nil {
					http.Error(w, fmt.Sprint(err), 409)
					return
				}
			} else {
				f.Unexpose(route.Local)
			}
			w.WriteHeader(http.StatusNoContent)
		})
	}
}
