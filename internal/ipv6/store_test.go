package ipv6

import (
	"net/netip"
	"path/filepath"
	"testing"
)

func TestStateRoundTripAndTrackIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.json")
	prefix := netip.MustParsePrefix("2001:db8:1::1234/64")
	if err := SaveState(path, State{}); err != nil {
		t.Fatalf("SaveState() error = %v", err)
	}
	if err := UpdateState(path, func(state *State) error {
		Track(state, "eth0", prefix)
		Track(state, "eth0", prefix)
		return nil
	}); err != nil {
		t.Fatalf("UpdateState() error = %v", err)
	}
	state, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState() error = %v", err)
	}
	if len(state.Addresses) != 1 || state.Addresses[0].Prefix != prefix.String() {
		t.Fatalf("unexpected state: %+v", state)
	}
	Untrack(&state, "eth0", prefix)
	if len(state.Addresses) != 0 {
		t.Fatalf("Untrack() left addresses: %+v", state.Addresses)
	}
}
