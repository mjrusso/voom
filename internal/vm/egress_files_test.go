package vm

import (
	"os"
	"testing"

	"github.com/mjrusso/voom/internal/state"
)

func TestEgressPublicationRepairAndIdempotence(t *testing.T) {
	st, _ := newTestStore(t)
	rt := st.Runtime(&state.VMRecord{ID: "egress-files"})
	ca := []byte("validated certificate bytes")
	if changed, err := publishEgress(rt, true, ca); err != nil || !changed {
		t.Fatalf("publish: %t %v", changed, err)
	}
	expected := []byte(`{
  "schemaVersion": 1,
  "mode": "explicit",
  "httpProxy": "http://192.168.127.1:3128",
  "httpsProxy": "http://192.168.127.1:3128",
  "caCertificate": "/run/voom/egress-ca.pem"
}
`)
	if ok, err := egressFileMatches(rt.EgressManifest(), expected); err != nil || !ok {
		t.Fatalf("manifest with CA: %v", err)
	}
	info, err := os.Stat(rt.EgressManifest())
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := egressFileMatches(rt.EgressCA(), ca); err != nil || !ok {
		t.Fatalf("CA: %v", err)
	}
	if changed, err := publishEgress(rt, true, ca); err != nil || changed {
		t.Fatalf("repeat: %t %v", changed, err)
	}
	next, err := os.Stat(rt.EgressManifest())
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(info, next) || !info.ModTime().Equal(next.ModTime()) {
		t.Fatal("healthy publication replaced manifest")
	}
	if err := os.Chmod(rt.EgressManifest(), 0600); err != nil {
		t.Fatal(err)
	}
	if changed, err := publishEgress(rt, true, ca); err != nil || !changed {
		t.Fatalf("repair: %t %v", changed, err)
	}
	if changed, err := publishEgress(rt, true, nil); err != nil || !changed {
		t.Fatalf("remove CA: %t %v", changed, err)
	}
	expected = []byte(`{
  "schemaVersion": 1,
  "mode": "explicit",
  "httpProxy": "http://192.168.127.1:3128",
  "httpsProxy": "http://192.168.127.1:3128"
}
`)
	if ok, err := egressFileMatches(rt.EgressManifest(), expected); err != nil || !ok {
		t.Fatalf("manifest: %v", err)
	}
	if _, err := os.Lstat(rt.EgressCA()); !os.IsNotExist(err) {
		t.Fatal("obsolete CA remains")
	}
	if changed, err := publishEgress(rt, false, nil); err != nil || !changed {
		t.Fatalf("disable: %t %v", changed, err)
	}
	if changed, err := publishEgress(rt, false, nil); err != nil || changed {
		t.Fatalf("repeat disable: %t %v", changed, err)
	}
}

func TestEgressPublicationReplacesGuestSymlink(t *testing.T) {
	st, _ := newTestStore(t)
	rt := st.Runtime(&state.VMRecord{ID: "symlink"})
	if _, err := publishEgress(rt, true, nil); err != nil {
		t.Fatal(err)
	}
	target := rt.EgressManifest() + ".target"
	if err := os.WriteFile(target, []byte("host data"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(rt.EgressManifest()); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, rt.EgressManifest()); err != nil {
		t.Fatal(err)
	}
	if changed, err := publishEgress(rt, true, nil); err != nil || !changed {
		t.Fatalf("symlink repair: %t %v", changed, err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "host data" {
		t.Fatalf("symlink target changed: %q %v", data, err)
	}
}
