package ipv6

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"
)

var (
	ErrAddressExists  = errors.New("address already exists")
	ErrAddressMissing = errors.New("address is not present")
)

type Address struct {
	Addr      netip.Addr `json:"addr"`
	PrefixLen int        `json:"prefix_len"`
	Scope     string     `json:"scope"`
	Permanent bool       `json:"permanent"`
}

func (a Address) Prefix() netip.Prefix { return netip.PrefixFrom(a.Addr, a.PrefixLen) }

type Interface struct {
	Name         string    `json:"name"`
	Index        int       `json:"index"`
	Up           bool      `json:"up"`
	PointToPoint bool      `json:"point_to_point"`
	Addresses    []Address `json:"addresses"`
	Route        *Route    `json:"route,omitempty"`
}

type Route struct {
	Gateway netip.Addr `json:"gateway"`
	Source  netip.Addr `json:"source"`
	OnLink  bool       `json:"on_link"`
}

type ManagedAddress struct {
	Interface string    `json:"interface"`
	Prefix    string    `json:"prefix"`
	CreatedAt time.Time `json:"created_at"`
}

type State struct {
	Version       int                     `json:"version"`
	Addresses     []ManagedAddress        `json:"addresses"`
	DefaultRoutes map[string]ManagedRoute `json:"default_routes,omitempty"`
}

type ManagedRoute struct {
	Gateway string `json:"gateway,omitempty"`
	Source  string `json:"source"`
	OnLink  bool   `json:"on_link,omitempty"`
}

type Manager interface {
	List() ([]Interface, error)
	Add(interfaceName string, prefix netip.Prefix) error
	Delete(interfaceName string, prefix netip.Prefix) error
	SetDefaultRoute(interfaceName string, gateway, source netip.Addr, onLink bool) error
	ClearDefaultRoute(interfaceName string) error
}

func ParsePrefix(raw string, defaultBits int) (netip.Prefix, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return netip.Prefix{}, errors.New("address is empty")
	}
	if strings.Contains(raw, "/") {
		p, err := netip.ParsePrefix(raw)
		if err != nil {
			return netip.Prefix{}, err
		}
		if !p.Addr().Is6() || p.Addr().Is4In6() {
			return netip.Prefix{}, fmt.Errorf("%q is not an IPv6 prefix", raw)
		}
		return p, nil
	}
	a, err := netip.ParseAddr(raw)
	if err != nil || !a.Is6() || a.Is4In6() {
		return netip.Prefix{}, fmt.Errorf("%q is not an IPv6 address", raw)
	}
	return netip.PrefixFrom(a, defaultBits), nil
}
