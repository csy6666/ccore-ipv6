//go:build linux

package ipv6

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

type LinuxManager struct{}

func NewManager() Manager { return LinuxManager{} }

func addressFromIP(ip net.IP) (netip.Addr, bool) {
	addr, ok := netip.AddrFromSlice(ip.To16())
	return addr.Unmap(), ok
}

func (LinuxManager) List() ([]Interface, error) {
	links, err := netlink.LinkList()
	if err != nil {
		return nil, fmt.Errorf("list interfaces: %w", err)
	}
	result := make([]Interface, 0, len(links))
	for _, link := range links {
		attrs := link.Attrs()
		if attrs == nil || attrs.Flags&net.FlagLoopback != 0 {
			continue
		}
		raw, err := netlink.AddrList(link, netlink.FAMILY_V6)
		if err != nil {
			return nil, fmt.Errorf("list IPv6 addresses on %s: %w", attrs.Name, err)
		}
		item := Interface{Name: attrs.Name, Index: attrs.Index, Up: attrs.Flags&net.FlagUp != 0, PointToPoint: attrs.Flags&net.FlagPointToPoint != 0}
		for _, addr := range raw {
			ip, ok := addressFromIP(addr.IP)
			if !ok || !ip.Is6() || ip.Is4In6() {
				continue
			}
			bits, _ := addr.Mask.Size()
			scope := "global"
			if ip.IsLinkLocalUnicast() {
				scope = "link"
			} else if ip.IsLoopback() {
				scope = "host"
			}
			item.Addresses = append(item.Addresses, Address{Addr: ip, PrefixLen: bits, Scope: scope, Permanent: addr.Flags&unix.IFA_F_PERMANENT != 0})
		}
		sort.Slice(item.Addresses, func(i, j int) bool { return item.Addresses[i].Addr.Less(item.Addresses[j].Addr) })
		if len(item.Addresses) > 0 {
			if route := defaultRoute(attrs.Index); route != nil {
				item.Route = route
			}
			result = append(result, item)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Index < result[j].Index })
	return result, nil
}

func defaultRoute(index int) *Route {
	routes, err := netlink.RouteList(nil, netlink.FAMILY_V6)
	if err != nil {
		return nil
	}
	var best *Route
	var bestMetric int
	for _, route := range routes {
		if route.LinkIndex != index {
			continue
		}
		if route.Dst != nil {
			bits, _ := route.Dst.Mask.Size()
			if bits != 0 || !route.Dst.IP.Equal(net.IPv6zero) {
				continue
			}
		}
		item := &Route{OnLink: route.Flags&int(unix.RTNH_F_ONLINK) != 0}
		if route.Gw != nil {
			if gateway, ok := addressFromIP(route.Gw); ok {
				item.Gateway = gateway
			}
		}
		if route.Src != nil {
			if source, ok := addressFromIP(route.Src); ok {
				item.Source = source
			}
		}
		if best == nil || route.Priority < bestMetric {
			best, bestMetric = item, route.Priority
		}
	}
	return best
}

func linkByName(name string) (netlink.Link, error) {
	link, err := netlink.LinkByName(name)
	if err != nil {
		return nil, fmt.Errorf("find interface %q: %w", name, err)
	}
	return link, nil
}

func ipNet(prefix netip.Prefix) *net.IPNet {
	return &net.IPNet{IP: net.IP(prefix.Addr().AsSlice()), Mask: net.CIDRMask(prefix.Bits(), 128)}
}

func (LinuxManager) Add(name string, prefix netip.Prefix) error {
	link, err := linkByName(name)
	if err != nil {
		return err
	}
	err = netlink.AddrAdd(link, &netlink.Addr{IPNet: ipNet(prefix), Flags: unix.IFA_F_NOPREFIXROUTE})
	if err != nil {
		if errors.Is(err, unix.EEXIST) {
			return fmt.Errorf("%s on %s: %w", prefix, name, ErrAddressExists)
		}
		return fmt.Errorf("add %s to %s: %w", prefix, name, err)
	}
	if err := waitSourceUsable(link, prefix.Addr()); err != nil {
		_ = netlink.AddrDel(link, &netlink.Addr{IPNet: ipNet(prefix)})
		return fmt.Errorf("address %s on %s is not usable: %w", prefix, name, err)
	}
	return nil
}

func (LinuxManager) Delete(name string, prefix netip.Prefix) error {
	link, err := linkByName(name)
	if err != nil {
		return err
	}
	if err := netlink.AddrDel(link, &netlink.Addr{IPNet: ipNet(prefix)}); err != nil {
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.EADDRNOTAVAIL) {
			return fmt.Errorf("%s on %s: %w", prefix, name, ErrAddressMissing)
		}
		return fmt.Errorf("delete %s from %s: %w", prefix, name, err)
	}
	return nil
}

func waitSourceUsable(link netlink.Link, source netip.Addr) error {
	deadline := time.Now().Add(8 * time.Second)
	for {
		addresses, err := netlink.AddrList(link, netlink.FAMILY_V6)
		if err != nil {
			return fmt.Errorf("list source address: %w", err)
		}
		found := false
		for _, address := range addresses {
			ip, ok := addressFromIP(address.IP)
			if !ok || ip != source {
				continue
			}
			found = true
			if address.Flags&unix.IFA_F_DADFAILED != 0 {
				return fmt.Errorf("source address %s failed duplicate address detection", source)
			}
			if address.Flags&unix.IFA_F_TENTATIVE == 0 {
				return nil
			}
		}
		if !found {
			return fmt.Errorf("source address %s is not on %s", source, link.Attrs().Name)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("source address %s did not finish duplicate address detection", source)
		}
		time.Sleep(150 * time.Millisecond)
	}
}

func (LinuxManager) SetDefaultRoute(name string, gateway, source netip.Addr, onLink bool) error {
	link, err := linkByName(name)
	if err != nil {
		return err
	}
	if !source.Is6() || source.Is4In6() {
		return errors.New("source must be a valid IPv6 address")
	}
	if err := waitSourceUsable(link, source); err != nil {
		return err
	}
	route := &netlink.Route{
		LinkIndex: link.Attrs().Index,
		Dst:       &net.IPNet{IP: net.IPv6zero, Mask: net.CIDRMask(0, 128)},
		Src:       net.IP(source.AsSlice()),
		Family:    netlink.FAMILY_V6,
	}
	if gateway.IsValid() {
		if !gateway.Is6() || gateway.Is4In6() {
			return errors.New("gateway must be a valid IPv6 address")
		}
		route.Gw = net.IP(gateway.AsSlice())
		if onLink {
			route.Flags = int(unix.RTNH_F_ONLINK)
		}
	}
	if err := netlink.RouteReplace(route); err != nil {
		return fmt.Errorf("replace IPv6 default route: %w", err)
	}
	return nil
}

func (LinuxManager) ClearDefaultRoute(name string) error {
	link, err := linkByName(name)
	if err != nil {
		return err
	}
	current := defaultRoute(link.Attrs().Index)
	if current == nil {
		return errors.New("no IPv6 default route found")
	}
	if !current.Gateway.IsValid() && link.Attrs().Flags&net.FlagPointToPoint == 0 {
		return errors.New("default route has no gateway; refusing to replace it automatically")
	}
	route := &netlink.Route{
		LinkIndex: link.Attrs().Index,
		Dst:       &net.IPNet{IP: net.IPv6zero, Mask: net.CIDRMask(0, 128)},
		Family:    netlink.FAMILY_V6,
	}
	if current.Gateway.IsValid() {
		route.Gw = net.IP(current.Gateway.AsSlice())
		if current.OnLink {
			route.Flags = int(unix.RTNH_F_ONLINK)
		}
	}
	if err := netlink.RouteReplace(route); err != nil {
		return fmt.Errorf("clear IPv6 default route source: %w", err)
	}
	return nil
}
