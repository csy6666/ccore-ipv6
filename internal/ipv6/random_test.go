package ipv6

import (
	"net/netip"
	"testing"
)

func TestGenerateStaysInsidePrefixAndHonorsExclusions(t *testing.T) {
	prefix := netip.MustParsePrefix("2001:db8:42::/64")
	exclude := map[netip.Addr]struct{}{netip.MustParseAddr("2001:db8:42::1234"): {}}
	addresses, err := Generate(prefix, 32, exclude)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	seen := make(map[netip.Addr]struct{}, len(addresses))
	for _, address := range addresses {
		if !prefix.Contains(address) {
			t.Fatalf("generated %s outside %s", address, prefix)
		}
		if address == prefix.Addr() {
			t.Fatalf("generated network address %s", address)
		}
		if _, exists := exclude[address]; exists {
			t.Fatalf("generated excluded address %s", address)
		}
		if _, duplicate := seen[address]; duplicate {
			t.Fatalf("generated duplicate address %s", address)
		}
		seen[address] = struct{}{}
	}
}

func TestGenerateRejectsInvalidCountAndTinyPrefix(t *testing.T) {
	if _, err := Generate(netip.MustParsePrefix("2001:db8::/64"), 0, nil); err == nil {
		t.Fatal("Generate() accepted zero count")
	}
	if _, err := RandomAddress(netip.MustParsePrefix("2001:db8::/127")); err == nil {
		t.Fatal("RandomAddress() accepted a prefix with fewer than two host bits")
	}
}

func TestParsePrefixMasksAddressOnlyInput(t *testing.T) {
	prefix, err := ParsePrefix("2001:db8:1::1234", 64)
	if err != nil {
		t.Fatalf("ParsePrefix() error = %v", err)
	}
	if got, want := prefix.String(), "2001:db8:1::1234/64"; got != want {
		t.Fatalf("ParsePrefix() = %s, want %s", got, want)
	}
	if _, err := ParsePrefix("192.0.2.1", 64); err == nil {
		t.Fatal("ParsePrefix() accepted an IPv4 address")
	}
}
