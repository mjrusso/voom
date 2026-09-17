package egress

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func certificate(t *testing.T, ca bool) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour), BasicConstraintsValid: true, IsCA: ca}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestCertificateValidation(t *testing.T) {
	ca, leaf := certificate(t, true), certificate(t, false)
	key := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("not a certificate")})
	cases := []struct {
		name  string
		data  []byte
		valid bool
	}{
		{"CA", ca, true}, {"bundle", append(append([]byte{}, ca...), leaf...), true},
		{"empty", nil, false}, {"leaf", leaf, false}, {"private key", key, false},
		{"mixed", append(append([]byte{}, ca...), key...), false},
		{"trailing", append(append([]byte{}, ca...), []byte("garbage")...), false},
		{"prefix", append([]byte("garbage"), ca...), false},
		{"broken", []byte("-----BEGIN CERTIFICATE-----\ninvalid\n-----END CERTIFICATE-----"), false},
		{"nested", append([]byte("-----BEGIN CERTIFICATE-----\ninvalid\n"), ca...), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateCA(tc.data); (err == nil) != tc.valid {
				t.Fatalf("valid=%t: %v", tc.valid, err)
			}
		})
	}
}

func TestSocketNormalizationAndValidation(t *testing.T) {
	dir, err := os.MkdirTemp("", "egress-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	actual := filepath.Join(dir, "actual")
	if err := os.Mkdir(actual, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias")
	if err := os.Symlink(actual, alias); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(actual, "proxy.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	d, err := Normalize(filepath.Join(alias, "proxy.sock"), "")
	if err != nil || d.BackendSocket != path {
		t.Fatalf("normalize: %+v %v", d, err)
	}
	if err := ProbeSocket(context.Background(), d.BackendSocket); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(actual, "linked.sock")
	if err := os.Symlink(path, symlink); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "relative", path + "\x00", "/" + strings.Repeat("x", 200), symlink, actual, filepath.Join(actual, "missing")} {
		if err := ProbeSocket(context.Background(), bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	regular := filepath.Join(actual, "file")
	if err := os.WriteFile(regular, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ProbeSocket(context.Background(), regular); err == nil {
		t.Fatal("accepted regular file")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ProbeSocket(ctx, path); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestReadCA(t *testing.T) {
	ca := certificate(t, true)
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, ca, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadCA(path)
	if err != nil || string(got) != string(ca) {
		t.Fatalf("CA bytes changed: %v", err)
	}
	if _, err := ReadCA(filepath.Dir(path)); err == nil {
		t.Fatal("accepted directory")
	}
	if _, err := ReadCA(path + "missing"); err == nil {
		t.Fatal("accepted missing CA")
	}
}
