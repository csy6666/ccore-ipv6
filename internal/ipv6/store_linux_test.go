//go:build linux

package ipv6

import (
	"fmt"
	"net/netip"
	"path/filepath"
	"sync"
	"testing"
)

func TestStateUpdatesSerializeAcrossGoroutines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := SaveState(path, State{}); err != nil {
		t.Fatalf("SaveState() error = %v", err)
	}
	var group sync.WaitGroup
	for i := 1; i <= 8; i++ {
		i := i
		group.Add(1)
		go func() {
			defer group.Done()
			prefix := netip.MustParsePrefix(fmt.Sprintf("2001:db8:1::%x/128", i))
			if err := UpdateState(path, func(state *State) error {
				Track(state, "eth0", prefix)
				return nil
			}); err != nil {
				t.Errorf("UpdateState() error = %v", err)
			}
		}()
	}
	group.Wait()
	state, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState() error = %v", err)
	}
	if len(state.Addresses) != 8 {
		t.Fatalf("concurrent updates left %d addresses, want 8", len(state.Addresses))
	}
}
