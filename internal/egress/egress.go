// Package egress validates non-secret proxy attachments and guest metadata.
package egress

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Explicit proxy endpoints are fixed by the guest metadata contract.
const (
	ModeExplicit  = "explicit"
	Listener      = "192.168.127.1:3128"
	GuestManifest = "/run/voom/egress.json"
	GuestCA       = "/run/voom/egress-ca.pem"
)

// Decl is the persistent attachment configuration.
type Decl struct {
	Mode          string `json:"mode"`
	Enabled       bool   `json:"enabled"`
	BackendSocket string `json:"backendSocket"`
	CACertPath    string `json:"caCertPath,omitempty"`
}

type manifest struct {
	SchemaVersion int    `json:"schemaVersion"`
	Mode          string `json:"mode"`
	HTTPProxy     string `json:"httpProxy"`
	HTTPSProxy    string `json:"httpsProxy"`
	CACertificate string `json:"caCertificate,omitempty"`
}

// Syntax checks stored declarations without opening external resources.
func Syntax(d Decl) error {
	if d.Mode != ModeExplicit {
		return fmt.Errorf("unsupported egress mode %q", d.Mode)
	}
	if err := socketSyntax(d.BackendSocket); err != nil {
		return err
	}
	if d.CACertPath != "" && (!filepath.IsAbs(d.CACertPath) || strings.ContainsRune(d.CACertPath, 0)) {
		return errors.New("CA path must be absolute and contain no NUL bytes")
	}
	return nil
}

func socketSyntax(path string) error {
	if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		return errors.New("backend socket must be an absolute filesystem path without NUL bytes")
	}
	var addr unix.RawSockaddrUnix
	if len(path) >= len(addr.Path) {
		return errors.New("backend socket path exceeds host Unix socket limit")
	}
	return nil
}

// Normalize resolves backend parents while preserving the configured CA path.
func Normalize(socket, ca string) (Decl, error) {
	d := Decl{Mode: ModeExplicit, Enabled: true, BackendSocket: socket}
	if err := socketSyntax(socket); err != nil {
		return d, err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(socket))
	if err != nil {
		return d, err
	}
	d.BackendSocket = filepath.Join(parent, filepath.Base(socket))
	if ca != "" {
		d.CACertPath, err = filepath.Abs(ca)
		if err != nil {
			return d, err
		}
	}
	return d, Syntax(d)
}

// ProbeSocket rejects changed path resolution and probes the Unix stream without sending data.
func ProbeSocket(ctx context.Context, path string) error {
	if err := socketSyntax(path); err != nil {
		return err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return err
	}
	if filepath.Join(parent, filepath.Base(path)) != path {
		return errors.New("backend socket parent resolution changed; set the attachment again")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("backend must be a Unix socket, not a symlink or other file")
	}
	conn, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", path)
	if err != nil {
		return fmt.Errorf("backend stream connection probe: %w", err)
	}
	return conn.Close()
}

// ReadCA reads and validates one public certificate bundle.
func ReadCA(path string) ([]byte, error) {
	if path == "" {
		return nil, nil
	}
	file, err := os.OpenFile(path, os.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("CA certificate must be a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, 4<<20+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 4<<20 {
		return nil, errors.New("CA certificate bundle exceeds 4 MiB")
	}
	if err := ValidateCA(data); err != nil {
		return nil, err
	}
	return data, nil
}

// ValidateCA requires certificates only, including at least one CA.
func ValidateCA(data []byte) error {
	remaining := bytes.TrimSpace(data)
	hasCA := false
	for len(remaining) > 0 {
		if !bytes.HasPrefix(remaining, []byte("-----BEGIN CERTIFICATE-----")) {
			return errors.New("CA bundle must contain only PEM CERTIFICATE blocks")
		}
		end := bytes.Index(remaining, []byte("-----END CERTIFICATE-----"))
		if end < 0 {
			return errors.New("malformed certificate PEM")
		}
		end += len("-----END CERTIFICATE-----")
		if bytes.Count(remaining[:end], []byte("-----BEGIN")) != 1 {
			return errors.New("malformed certificate PEM")
		}
		block, rest := pem.Decode(remaining[:end])
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 || len(rest) != 0 {
			return errors.New("malformed certificate PEM")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return fmt.Errorf("invalid X.509 certificate: %w", err)
		}
		hasCA = hasCA || cert.IsCA
		remaining = bytes.TrimSpace(remaining[end:])
	}
	if !hasCA {
		return errors.New("certificate bundle must contain a CA certificate")
	}
	return nil
}

// ManifestBytes builds metadata for already validated CA bytes.
func ManifestBytes(ca []byte) []byte {
	m := manifest{SchemaVersion: 1, Mode: ModeExplicit, HTTPProxy: "http://" + Listener, HTTPSProxy: "http://" + Listener}
	if len(ca) > 0 {
		m.CACertificate = GuestCA
	}
	data, _ := json.MarshalIndent(m, "", "  ")
	return append(data, '\n')
}
