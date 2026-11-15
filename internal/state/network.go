package state

import (
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
)

// NetworkConfig contains global guest network exceptions, applied at VM startup.
type NetworkConfig struct {
	AllowedDestinations []netip.AddrPort `json:"allowedDestinations"`
}

func (s *Store) networkConfigPath() string {
	return filepath.Join(s.paths.Config, "network.json")
}

func normalizeAllowedDestinations(destinations []netip.AddrPort) ([]netip.AddrPort, error) {
	result := make([]netip.AddrPort, 0, len(destinations))
	seen := make(map[netip.AddrPort]bool)
	for i, dest := range destinations {
		if !dest.IsValid() {
			return nil, fmt.Errorf("allowedDestinations[%d]: destination is empty or null", i)
		}
		if dest.Port() == 0 {
			return nil, fmt.Errorf("allowedDestinations[%d]: port must be between 1 and 65535", i)
		}
		if dest.Addr().Zone() != "" {
			return nil, fmt.Errorf("allowedDestinations[%d]: IPv6 zone identifiers are not allowed", i)
		}
		normalized := netip.AddrPortFrom(dest.Addr().Unmap(), dest.Port())
		if !seen[normalized] {
			result = append(result, normalized)
			seen[normalized] = true
		}
	}
	return result, nil
}

// LoadNetworkConfig reads network.json; a missing file allows no exceptions.
func (s *Store) LoadNetworkConfig() (NetworkConfig, error) {
	var config NetworkConfig
	path := s.networkConfigPath()
	if err := ReadJSON(path, &config); err != nil && !os.IsNotExist(err) {
		return NetworkConfig{}, fmt.Errorf("read %s: %w", path, err)
	}
	normalized, err := normalizeAllowedDestinations(config.AllowedDestinations)
	if err != nil {
		return NetworkConfig{}, fmt.Errorf("invalid network config %s: %w", path, err)
	}
	config.AllowedDestinations = normalized
	return config, nil
}

// SetAllowedDestinations replaces global exceptions for subsequent VM starts.
func (s *Store) SetAllowedDestinations(destinations []string) (NetworkConfig, error) {
	parsed := make([]netip.AddrPort, 0, len(destinations))
	for _, raw := range destinations {
		destination, err := netip.ParseAddrPort(raw)
		if err != nil {
			return NetworkConfig{}, fmt.Errorf("invalid allowed destination %q: %w", raw, err)
		}
		parsed = append(parsed, destination)
	}
	normalized, err := normalizeAllowedDestinations(parsed)
	if err != nil {
		return NetworkConfig{}, err
	}
	config := NetworkConfig{AllowedDestinations: normalized}
	err = s.WithGlobal(func() error {
		path := s.networkConfigPath()
		info, err := os.Lstat(path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if info != nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink; edit the configuration that creates it", path)
		}
		return WriteJSONAtomic(path, config)
	})
	if err != nil {
		return NetworkConfig{}, err
	}
	return config, nil
}
