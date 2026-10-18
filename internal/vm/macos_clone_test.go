package vm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/mjrusso/voom/internal/state"
)

func TestMacOSCloneIdentityAndRollback(t *testing.T) {
	for _, mode := range []string{"fresh", "failure", "unchanged", "collision", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			st, _ := newTestStore(t)
			hash := sha256.Sum256([]byte("source-identity"))
			src := &state.VMRecord{SchemaVersion: 2, ID: "source", Name: "source", Driver: "vfkit", Arch: "aarch64-darwin", MacOS: &state.MacOSPlatform{IdentitySHA256: hex.EncodeToString(hash[:]), CPUs: 4, MemoryMiB: 8192, MinimumCPUs: 4, MinimumMemoryMiB: 8192, Version: "27.0.1", Build: "26A434"}, Resources: state.VMResources{CPUs: 4, MemoryMiB: 8192}, Network: state.VMNetwork{SSHPort: 2222, SSHBind: "127.0.0.1"}}
			src.StorageGeneration = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			if err := os.MkdirAll(st.VMStorageDir(src), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := st.SaveVM(src); err != nil {
				t.Fatal(err)
			}
			if err := st.RegisterVM(src.Name, src.ID); err != nil {
				t.Fatal(err)
			}
			files := map[string]string{"disk.raw": "installed disk", "MachineIdentifier": "source-identity", "HardwareModel": "hardware", "AuxiliaryStorage": "nvram"}
			for name, data := range files {
				if err := os.WriteFile(filepath.Join(st.VMStorageDir(src), name), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			helper := filepath.Join(t.TempDir(), "identity-helper")
			script := "#!/bin/sh\nprintf fresh-identity > \"$1\"\n"
			if mode == "failure" {
				script = "#!/bin/sh\nprintf partial > \"$1\"\nexit 1\n"
			}
			if mode == "unchanged" {
				script = "#!/bin/sh\nexit 0\n"
			}
			if err := os.WriteFile(helper, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("VOOM_MACOS_IDENTITY", helper)
			if mode == "collision" {
				other := *src
				other.ID, other.Name = "other", "other"
				platform := *src.MacOS
				sum := sha256.Sum256([]byte("fresh-identity"))
				platform.IdentitySHA256 = hex.EncodeToString(sum[:])
				other.MacOS = &platform
				other.Network.SSHPort = 2223
				if err := st.SaveVM(&other); err != nil {
					t.Fatal(err)
				}
				if err := st.RegisterVM(other.Name, other.ID); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadDir(filepath.Dir(st.VMDir(src.ID)))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			cloned, err := New(st).Clone(ctx, "source", "copy")
			if mode != "fresh" {
				if err == nil {
					t.Fatal("clone should fail")
				}
				if _, err := st.LoadVM("copy"); err == nil {
					t.Fatal("failed clone registered")
				}
				after, err := os.ReadDir(filepath.Dir(st.VMDir(src.ID)))
				if err != nil || len(after) != len(before) {
					t.Fatalf("failed clone leaked storage: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if cloned.SchemaVersion != 2 || cloned.StorageGeneration == src.StorageGeneration || cloned.ID == src.ID || cloned.Network.SSHPort == src.Network.SSHPort || cloned.MacOS.IdentitySHA256 == src.MacOS.IdentitySHA256 {
					t.Fatalf("clone reused identity: %+v", cloned)
				}
				if err := cloned.MacOS.ValidateStorage(st.VMStorageDir(cloned)); err != nil {
					t.Fatal(err)
				}
				saved, err := st.LoadVM("copy")
				if err != nil || saved.MacOS.IdentitySHA256 != cloned.MacOS.IdentitySHA256 {
					t.Fatalf("identity not persisted: %+v %v", saved, err)
				}
				for name, original := range files {
					copied, err := os.ReadFile(filepath.Join(st.VMStorageDir(cloned), name))
					if err != nil {
						t.Fatal(err)
					}
					if name != "MachineIdentifier" && string(copied) != original {
						t.Fatalf("clone changed %s", name)
					}
				}
				for _, name := range []string{"disk.raw", "AuxiliaryStorage"} {
					if err := os.WriteFile(filepath.Join(st.VMStorageDir(cloned), name), []byte("clone write"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			for name, expected := range files {
				data, err := os.ReadFile(filepath.Join(st.VMStorageDir(src), name))
				if err != nil || string(data) != expected {
					t.Fatalf("source %s changed: %q %v", name, data, err)
				}
			}
			saved, err := st.LoadVM("source")
			if err != nil || saved.MacOS.IdentitySHA256 != src.MacOS.IdentitySHA256 {
				t.Fatalf("source metadata changed: %+v %v", saved, err)
			}
		})
	}
}
