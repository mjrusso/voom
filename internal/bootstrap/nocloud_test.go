package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	diskfs "github.com/diskfs/go-diskfs"
)

func TestBuildNoCloudFilesIncludesRootAndLoginUser(t *testing.T) {
	files, err := BuildNoCloudFiles(SeedConfig{
		Hostname:       "scratch",
		InstanceID:     "voom-vm1",
		SSHUser:        "mjrusso",
		AuthorizedKeys: []string{"ssh-ed25519 AAAATEST user@example"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(files.MetaData); !strings.Contains(got, "instance-id: voom-vm1") || !strings.Contains(got, "local-hostname: scratch") {
		t.Fatalf("unexpected meta-data: %s", got)
	}
	userData := string(files.UserData)
	for _, want := range []string{
		"#cloud-config",
		"name: root",
		"name: mjrusso",
		"sudo: ALL=(ALL) NOPASSWD:ALL",
		"ssh_authorized_keys:",
		"ssh-ed25519 AAAATEST user@example",
	} {
		if !strings.Contains(userData, want) {
			t.Fatalf("user-data missing %q: %s", want, userData)
		}
	}
	networkConfig := string(files.NetworkConfig)
	for _, want := range []string{
		"version: 2",
		"name: en*",
		"name: eth*",
		"dhcp4: true",
	} {
		if !strings.Contains(networkConfig, want) {
			t.Fatalf("network-config missing %q: %s", want, networkConfig)
		}
	}
}

func TestBuildNoCloudFilesEmitsGuestHelpersPayloadWhenRequested(t *testing.T) {
	files, err := BuildNoCloudFiles(SeedConfig{
		Hostname:            "scratch",
		InstanceID:          "voom-vm1",
		SSHUser:             "debian",
		AuthorizedKeys:      []string{"ssh-ed25519 AAAATEST user@example"},
		InstallGuestHelpers: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	userData := string(files.UserData)
	for _, want := range []string{
		"packages:",
		"- jq",
		"- gawk",
		"- iproute2",
		"bootcmd:",
		"- modprobe",
		"write_files:",
		"/etc/modules-load.d/voom.conf",
		"/usr/local/sbin/voom-portfwd",
		"/usr/local/sbin/voom-mount-shares",
		"/etc/systemd/system/run-voom.mount",
		"/etc/systemd/system/voom-portfwd.service",
		"/etc/systemd/system/voom-mount-shares.service",
		"runcmd:",
		"daemon-reload",
		"run-voom.mount",
		"voom-portfwd.service",
		"voom-mount-shares.service",
	} {
		if !strings.Contains(userData, want) {
			t.Fatalf("user-data missing %q in helpers payload: %s", want, userData)
		}
	}
}

func TestBuildNoCloudFilesOmitsHelpersPayloadByDefault(t *testing.T) {
	files, err := BuildNoCloudFiles(SeedConfig{
		Hostname:       "scratch",
		InstanceID:     "voom-vm1",
		SSHUser:        "debian",
		AuthorizedKeys: []string{"ssh-ed25519 AAAATEST user@example"},
	})
	if err != nil {
		t.Fatal(err)
	}
	userData := string(files.UserData)
	for _, unwanted := range []string{"packages:", "write_files:", "runcmd:", "bootcmd:", "voom-portfwd"} {
		if strings.Contains(userData, unwanted) {
			t.Fatalf("user-data unexpectedly contains %q without InstallGuestHelpers: %s", unwanted, userData)
		}
	}
}

func TestDiscoverAuthorizedKeysUsesIdentityPubfile(t *testing.T) {
	dir := t.TempDir()
	identity := filepath.Join(dir, "id_test")
	if err := os.WriteFile(identity+".pub", []byte("ssh-ed25519 AAAATEST explicit@example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	keys, err := DiscoverAuthorizedKeys(identity)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0] != "ssh-ed25519 AAAATEST explicit@example" {
		t.Fatalf("unexpected keys: %#v", keys)
	}
}

func TestWriteNoCloudImageProducesReadableCIDATAFilesystem(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seed.img")
	err := WriteNoCloudImage(path, SeedConfig{
		Hostname:       "scratch",
		InstanceID:     "voom-vm1",
		SSHUser:        "root",
		AuthorizedKeys: []string{"ssh-ed25519 AAAATEST user@example"},
	})
	if err != nil {
		t.Fatal(err)
	}

	img, err := diskfs.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = img.Close() }()

	fs, err := img.GetFilesystem(0)
	if err != nil {
		t.Fatal(err)
	}
	if label := strings.TrimSpace(fs.Label()); label != "CIDATA" {
		t.Fatalf("filesystem label = %q, want CIDATA", label)
	}
	metaData, err := fs.ReadFile("/meta-data")
	if err != nil {
		t.Fatal(err)
	}
	userData, err := fs.ReadFile("/user-data")
	if err != nil {
		t.Fatal(err)
	}
	networkConfig, err := fs.ReadFile("/network-config")
	if err != nil {
		t.Fatal(err)
	}
	if got := string(metaData); !strings.Contains(got, "instance-id: voom-vm1") {
		t.Fatalf("unexpected meta-data: %s", got)
	}
	if got := string(userData); !strings.Contains(got, "#cloud-config") || !strings.Contains(got, "ssh-ed25519 AAAATEST user@example") {
		t.Fatalf("unexpected user-data: %s", got)
	}
	if got := string(networkConfig); !strings.Contains(got, "version: 2") || !strings.Contains(got, "dhcp4: true") {
		t.Fatalf("unexpected network-config: %s", got)
	}
}
