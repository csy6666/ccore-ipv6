package ipv6

import (
	"crypto/rand"
	"errors"
	"fmt"
	"net/netip"
)

func hostMask(index, bits int) byte {
	start := index * 8
	switch {
	case start >= bits:
		return 0xff
	case start+8 <= bits:
		return 0
	default:
		return byte(0xff) >> (bits - start)
	}
}

func reserved(p netip.Prefix, addr netip.Addr) bool {
	b := addr.As16()
	allZero := true
	for i := range b {
		mask := hostMask(i, p.Bits())
		if mask != 0 && b[i]&mask != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		return true
	}
	if 128-p.Bits() >= 8 {
		allHigh := true
		for i := range b {
			mask := hostMask(i, p.Bits())
			if mask == 0 {
				continue
			}
			want := mask
			if i == len(b)-1 {
				want &^= 0x7f
			}
			if b[i]&want != want {
				allHigh = false
				break
			}
		}
		if allHigh {
			return true
		}
	}
	if p.Bits() == 64 && b[8] == 0xfd {
		allHigh := true
		for i := 9; i <= 14; i++ {
			if b[i] != 0xff {
				allHigh = false
				break
			}
		}
		if allHigh && b[15]&0x80 != 0 {
			return true
		}
	}
	return false
}

func RandomAddress(prefix netip.Prefix) (netip.Addr, error) {
	prefix = prefix.Masked()
	if !prefix.Addr().Is6() || prefix.Addr().Is4In6() {
		return netip.Addr{}, errors.New("only IPv6 prefixes are supported")
	}
	if 128-prefix.Bits() < 2 {
		return netip.Addr{}, errors.New("prefix has fewer than two host bits")
	}
	base := prefix.Addr().As16()
	for attempt := 0; attempt < 64; attempt++ {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return netip.Addr{}, fmt.Errorf("read random source: %w", err)
		}
		candidate := base
		for i := range candidate {
			candidate[i] |= random[i] & hostMask(i, prefix.Bits())
		}
		addr := netip.AddrFrom16(candidate)
		if !reserved(prefix, addr) {
			return addr, nil
		}
	}
	return netip.Addr{}, errors.New("could not find an available address")
}

func Generate(prefix netip.Prefix, count int, exclude map[netip.Addr]struct{}) ([]netip.Addr, error) {
	if count <= 0 {
		return nil, errors.New("count must be positive")
	}
	if exclude == nil {
		exclude = map[netip.Addr]struct{}{}
	}
	seen := make(map[netip.Addr]struct{}, count)
	result := make([]netip.Addr, 0, count)
	for budget := count*16 + 1024; len(result) < count && budget > 0; budget-- {
		addr, err := RandomAddress(prefix)
		if err != nil {
			return nil, err
		}
		if _, ok := exclude[addr]; ok {
			continue
		}
		if _, ok := seen[addr]; ok {
			continue
		}
		seen[addr] = struct{}{}
		result = append(result, addr)
	}
	if len(result) != count {
		return result, fmt.Errorf("prefix has too few available addresses: generated %d of %d", len(result), count)
	}
	return result, nil
}
