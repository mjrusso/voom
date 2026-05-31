// Package state owns voom's on-disk layout: VM records, image records,
// the central index, runtime paths, and cross-process locking.
package state

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mjrusso/voom/internal/host"
)

// Option customizes a Store opened by Open.
type Option func(*Store)

// WithHostPortAvailable overrides the host-port availability probe used by SSH
// port allocation and validation.
func WithHostPortAvailable(fn HostPortAvailableFunc) Option {
	return func(s *Store) {
		if fn != nil {
			s.hostPortAvailable = fn
		}
	}
}

// Open resolves host paths, ensures state directories exist, and loads the index from disk
// (creating an empty one if missing).
func Open(opts ...Option) (*Store, error) {
	p := host.ResolvePaths()
	if err := host.EnsureRuntimeDir(p.Runtime); err != nil {
		return nil, err
	}
	for _, d := range []string{p.Config, p.State, p.Cache, filepath.Join(p.State, "locks"), filepath.Join(p.State, "locks", "vms")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	st := &Store{paths: p, index: Index{SchemaVersion: SchemaVersion, VMs: map[string]string{}, Images: map[string]string{}}, hostPortAvailable: defaultHostPortAvailable}
	for _, opt := range opts {
		opt(st)
	}
	err := st.reloadIndex()
	if os.IsNotExist(err) {
		return st, st.SaveIndex()
	}
	if err != nil {
		return nil, err
	}
	return st, nil
}

func (s *Store) reloadIndex() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(filepath.Join(s.paths.State, "state.json"))
	if err != nil {
		return err
	}
	var index Index
	if err := json.Unmarshal(b, &index); err != nil {
		return err
	}
	if index.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported state schema version %d", index.SchemaVersion)
	}
	if index.VMs == nil {
		index.VMs = map[string]string{}
	}
	if index.Images == nil {
		index.Images = map[string]string{}
	}
	s.index = index
	return nil
}

// Paths returns the resolved host paths backing this store.
func (s *Store) Paths() host.Paths {
	return s.paths
}

// IndexSnapshot returns a deep copy of the current in-memory index.
func (s *Store) IndexSnapshot() Index {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

func (s *Store) snapshotLocked() Index {
	out := Index{
		SchemaVersion: s.index.SchemaVersion,
		VMs:           make(map[string]string, len(s.index.VMs)),
		Images:        make(map[string]string, len(s.index.Images)),
	}
	for k, v := range s.index.VMs {
		out.VMs[k] = v
	}
	for k, v := range s.index.Images {
		out.Images[k] = v
	}
	return out
}

// SaveIndex atomically writes the current index to state.json on disk.
func (s *Store) SaveIndex() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveIndexLocked()
}

func (s *Store) saveIndexLocked() error {
	if s.index.SchemaVersion == 0 {
		s.index.SchemaVersion = SchemaVersion
	}
	return WriteJSONAtomic(filepath.Join(s.paths.State, "state.json"), &s.index)
}

// SetIndexSnapshot replaces the in-memory index with a deep copy of next and persists it.
func (s *Store) SetIndexSnapshot(next Index) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.index = Index{
		SchemaVersion: next.SchemaVersion,
		VMs:           map[string]string{},
		Images:        map[string]string{},
	}
	for k, v := range next.VMs {
		s.index.VMs[k] = v
	}
	for k, v := range next.Images {
		s.index.Images[k] = v
	}
	return s.saveIndexLocked()
}

// NewID returns a random 128-bit identifier as an uppercase hex string.
func NewID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return strings.ToUpper(hex.EncodeToString(b[:])), nil
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
