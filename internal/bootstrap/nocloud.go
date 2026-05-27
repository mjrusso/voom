package bootstrap

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	diskfs "github.com/diskfs/go-diskfs"
	"github.com/diskfs/go-diskfs/disk"
	"github.com/diskfs/go-diskfs/filesystem"
	"gopkg.in/yaml.v3"
)

const (
	seedImageSize = 64 * 1024 * 1024
	seedVolumeID  = "CIDATA"
)

// SeedConfig is the input for building a NoCloud seed: identity, hostname, and whether to install voom's guest helpers.
type SeedConfig struct {
	Hostname            string
	InstanceID          string
	SSHUser             string
	AuthorizedKeys      []string
	InstallGuestHelpers bool
}

// NoCloudFiles holds the three byte payloads written to the NoCloud seed image (meta-data, user-data, network-config).
type NoCloudFiles struct {
	MetaData      []byte
	UserData      []byte
	NetworkConfig []byte
}

// DiscoverAuthorizedKeys returns the SSH public keys to authorize: the .pub for identityPath if given, otherwise the common ~/.ssh/id_*.pub files.
func DiscoverAuthorizedKeys(identityPath string) ([]string, error) {
	var paths []string
	if identityPath != "" {
		if strings.HasSuffix(identityPath, ".pub") {
			paths = append(paths, identityPath)
		} else {
			paths = append(paths, identityPath+".pub")
		}
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		sshDir := filepath.Join(home, ".ssh")
		for _, name := range []string{"id_ed25519.pub", "id_ecdsa.pub", "id_rsa.pub", "id_dsa.pub"} {
			paths = append(paths, filepath.Join(sshDir, name))
		}
	}

	keys := make([]string, 0, len(paths))
	seen := map[string]struct{}{}
	for _, path := range paths {
		key, err := readAuthorizedKey(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	if len(keys) > 0 {
		return keys, nil
	}
	if identityPath != "" {
		return nil, fmt.Errorf("no readable SSH public key found for %q; expected %s", identityPath, identityPath+".pub")
	}
	return nil, errors.New("no SSH public keys found in ~/.ssh; create a default SSH key or import image metadata with sshIdentityPath")
}

// BuildNoCloudFiles assembles the meta-data, user-data, and network-config YAML payloads for cfg.
func BuildNoCloudFiles(cfg SeedConfig) (*NoCloudFiles, error) {
	hostname := strings.TrimSpace(cfg.Hostname)
	if hostname == "" {
		return nil, errors.New("hostname is required for NoCloud bootstrap")
	}
	instanceID := strings.TrimSpace(cfg.InstanceID)
	if instanceID == "" {
		return nil, errors.New("instance ID is required for NoCloud bootstrap")
	}
	user := strings.TrimSpace(cfg.SSHUser)
	if user == "" {
		user = "root"
	}
	keys := dedupeStrings(cfg.AuthorizedKeys)
	if len(keys) == 0 {
		return nil, errors.New("at least one SSH public key is required for NoCloud bootstrap")
	}

	metaData, err := yaml.Marshal(map[string]any{
		"instance-id":    instanceID,
		"local-hostname": hostname,
	})
	if err != nil {
		return nil, err
	}

	users := []map[string]any{
		{
			"name":                "root",
			"ssh_authorized_keys": keys,
		},
	}
	if user != "root" {
		users = append(users, map[string]any{
			"name":                user,
			"sudo":                "ALL=(ALL) NOPASSWD:ALL",
			"ssh_authorized_keys": keys,
		})
	}
	userDataDoc := map[string]any{
		"hostname":          hostname,
		"preserve_hostname": false,
		"ssh_pwauth":        false,
		"disable_root":      false,
		"users":             users,
	}
	if cfg.InstallGuestHelpers {
		writeFiles := make([]map[string]any, 0, len(helperFiles()))
		for _, f := range helperFiles() {
			writeFiles = append(writeFiles, map[string]any{
				"path":        f.Path,
				"permissions": f.Permissions,
				"content":     f.Content,
			})
		}
		runcmd := [][]string{{"systemctl", "daemon-reload"}}
		for _, unit := range helperEnabledUnits {
			runcmd = append(runcmd, []string{"systemctl", "enable", "--now", unit})
		}
		userDataDoc["packages"] = helperPackages
		userDataDoc["bootcmd"] = [][]string{{"modprobe", "virtiofs"}}
		userDataDoc["write_files"] = writeFiles
		userDataDoc["runcmd"] = runcmd
	}
	userDataBody, err := yaml.Marshal(userDataDoc)
	if err != nil {
		return nil, err
	}
	userData := append([]byte("#cloud-config\n"), userDataBody...)
	networkConfig, err := yaml.Marshal(map[string]any{
		"version": 2,
		"ethernets": map[string]any{
			"en": map[string]any{
				"match": map[string]any{
					"name": "en*",
				},
				"dhcp4": true,
			},
			"eth": map[string]any{
				"match": map[string]any{
					"name": "eth*",
				},
				"dhcp4": true,
			},
		},
	})
	if err != nil {
		return nil, err
	}
	return &NoCloudFiles{MetaData: metaData, UserData: userData, NetworkConfig: networkConfig}, nil
}

// WriteNoCloudImage writes a FAT32 NoCloud seed disk at path containing meta-data, user-data, and network-config for cfg.
func WriteNoCloudImage(path string, cfg SeedConfig) error {
	files, err := BuildNoCloudFiles(cfg)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	_ = os.Remove(tmp)

	img, err := diskfs.Create(tmp, seedImageSize, diskfs.SectorSizeDefault)
	if err != nil {
		return err
	}
	defer func() { _ = img.Close() }()

	fs, err := img.CreateFilesystem(disk.FilesystemSpec{
		Partition:   0,
		FSType:      filesystem.TypeFat32,
		VolumeLabel: seedVolumeID,
	})
	if err != nil {
		return err
	}
	if err := writeFile(fs, "/meta-data", files.MetaData); err != nil {
		_ = fs.Close()
		return err
	}
	if err := writeFile(fs, "/user-data", files.UserData); err != nil {
		_ = fs.Close()
		return err
	}
	if err := writeFile(fs, "/network-config", files.NetworkConfig); err != nil {
		_ = fs.Close()
		return err
	}
	if err := fs.Close(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func writeFile(fs filesystem.FileSystem, path string, data []byte) error {
	f, err := fs.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func readAuthorizedKey(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	key := strings.TrimSpace(string(bytes.TrimSpace(b)))
	if key == "" {
		return "", fmt.Errorf("SSH public key file %s is empty", path)
	}
	return key, nil
}

func dedupeStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}
