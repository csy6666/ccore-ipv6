//go:build !linux

package ipv6

import (
	"errors"
	"net/netip"
)

var ErrUnsupported = errors.New("ccore-ipv6 currently supports Linux only")

type unsupportedManager struct{}

func NewManager() Manager                                    { return unsupportedManager{} }
func (unsupportedManager) List() ([]Interface, error)        { return nil, ErrUnsupported }
func (unsupportedManager) Add(string, netip.Prefix) error    { return ErrUnsupported }
func (unsupportedManager) Delete(string, netip.Prefix) error { return ErrUnsupported }
func (unsupportedManager) SetDefaultRoute(string, netip.Addr, netip.Addr, bool) error {
	return ErrUnsupported
}
