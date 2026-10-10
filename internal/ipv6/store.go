package ipv6

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"time"
)

func LoadState(path string) (State, error) {
	return loadStateUnlocked(path)
}

func SaveState(path string, state State) error {
	lock, err := acquireStateLock(path)
	if err != nil {
		return err
	}
	defer lock.Close()
	return saveStateUnlocked(path, state)
}

func UpdateState(path string, update func(*State) error) error {
	lock, err := acquireStateLock(path)
	if err != nil {
		return err
	}
	defer lock.Close()
	state, err := loadStateUnlocked(path)
	if err != nil {
		return err
	}
	if err := update(&state); err != nil {
		return err
	}
	return saveStateUnlocked(path, state)
}

func loadStateUnlocked(path string) (State, error) {
	state := State{Version: 1}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, fmt.Errorf("read state: %w", err)
	}
	if len(b) == 0 {
		return state, nil
	}
	if err := json.Unmarshal(b, &state); err != nil {
		return state, fmt.Errorf("decode state: %w", err)
	}
	if state.Version == 0 {
		state.Version = 1
	}
	return state, nil
}

func saveStateUnlocked(path string, state State) error {
	if state.Version == 0 {
		state.Version = 1
	}
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".state-*")
	if err != nil {
		return fmt.Errorf("create temporary state: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = tmp.Close(); _ = os.Remove(tmpName) }()
	// State contains only managed addresses and route metadata; the service
	// account must be able to read state even when an administrator runs the
	// mutating CLI as root. Credentials are never stored here.
	if err := tmp.Chmod(0o644); err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace state: %w", err)
	}
	return nil
}

func Track(state *State, interfaceName string, prefix netip.Prefix) {
	canonical := prefix.String()
	for _, item := range state.Addresses {
		if item.Interface == interfaceName && item.Prefix == canonical {
			return
		}
	}
	state.Addresses = append(state.Addresses, ManagedAddress{Interface: interfaceName, Prefix: canonical, CreatedAt: time.Now().UTC()})
}

func Untrack(state *State, interfaceName string, prefix netip.Prefix) {
	canonical := prefix.String()
	filtered := state.Addresses[:0]
	for _, item := range state.Addresses {
		if item.Interface == interfaceName && item.Prefix == canonical {
			continue
		}
		filtered = append(filtered, item)
	}
	state.Addresses = filtered
}
