package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/csy6666/ccore-ipv6/internal/api"
	"github.com/csy6666/ccore-ipv6/internal/dns/cloudflare"
	"github.com/csy6666/ccore-ipv6/internal/ipv6"
	"github.com/csy6666/ccore-ipv6/internal/version"
)

const defaultState = "/var/lib/ccore-ipv6/state.json"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "list":
		err = listCommand(os.Args[2:])
	case "generate":
		err = generateCommand(os.Args[2:])
	case "add":
		err = addCommand(os.Args[2:])
	case "remove":
		err = removeCommand(os.Args[2:])
	case "restore":
		err = restoreCommand(os.Args[2:])
	case "route-source":
		err = routeSourceCommand(os.Args[2:])
	case "serve":
		err = serveCommand(os.Args[2:])
	case "dns-zones":
		err = dnsZonesCommand(os.Args[2:])
	case "dns-sync":
		err = dnsSyncCommand(os.Args[2:])
	case "version":
		fmt.Printf("ccore-ipv6 %s (%s/%s) commit=%s built=%s\n", version.Version, runtime.GOOS, runtime.GOARCH, version.Commit, version.Date)
		return
	case "help", "-h", "--help":
		usage()
		return
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`ccore-ipv6

用法：
  ccore-ipv6 list
  ccore-ipv6 generate --prefix <ipv6-prefix> --count <n>
  ccore-ipv6 add --iface <name> [--prefix <ipv6-prefix> --count <n>] [address ...]
  ccore-ipv6 remove --iface <name> [--state <path>] address ...
  ccore-ipv6 route-source --iface <name> --source <ipv6> [--gateway <ipv6>]
  ccore-ipv6 restore [--state <path>]
  ccore-ipv6 serve [--socket /run/ccore-ipv6/ccore-ipv6.sock] [--state <path>]
  ccore-ipv6 dns-zones
  ccore-ipv6 dns-sync --zone-id <id> --zone-name <name> --name <template>

默认状态文件：/var/lib/ccore-ipv6/state.json
`)
}

func cloudflareClient() (*cloudflare.Client, error) {
	return cloudflare.New(strings.TrimSpace(os.Getenv("CCORE_IPV6_CF_TOKEN")))
}

func dnsZonesCommand(args []string) error {
	fs := flag.NewFlagSet("dns-zones", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	client, err := cloudflareClient()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	zones, err := client.ListZones(ctx)
	if err != nil {
		return err
	}
	for _, zone := range zones {
		fmt.Printf("%s\t%s\t%s\n", zone.ID, zone.Name, zone.Status)
	}
	return nil
}

func dnsSyncCommand(args []string) error {
	fs := flag.NewFlagSet("dns-sync", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	statePath := fs.String("state", defaultState, "state file")
	zoneID := fs.String("zone-id", "", "Cloudflare zone id")
	zoneName := fs.String("zone-name", "", "Cloudflare zone name")
	nameTemplate := fs.String("name", "", "record name template, e.g. node-{n}")
	ttl := fs.Int("ttl", 60, "DNS TTL in seconds")
	proxied := fs.Bool("proxied", false, "enable Cloudflare proxying")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*zoneID) == "" || strings.TrimSpace(*zoneName) == "" {
		return errors.New("--zone-id and --zone-name are required")
	}
	state, err := ipv6.LoadState(*statePath)
	if err != nil {
		return err
	}
	if len(state.Addresses) == 0 {
		return errors.New("state has no managed addresses")
	}
	addresses := make([]netip.Addr, 0, len(state.Addresses))
	for _, item := range state.Addresses {
		prefix, err := ipv6.ParsePrefix(item.Prefix, 64)
		if err != nil {
			return fmt.Errorf("state entry %s/%s: %w", item.Interface, item.Prefix, err)
		}
		addresses = append(addresses, prefix.Addr())
	}
	names, err := cloudflare.ExpandNames(*nameTemplate, *zoneName, len(addresses))
	if err != nil {
		return err
	}
	client, err := cloudflareClient()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	result, err := client.SyncAAAA(ctx, *zoneID, names, addresses, *ttl, *proxied)
	fmt.Printf("created=%d skipped=%d failed=%d\n", result.Created, result.Skipped, result.Failed)
	return err
}

func listCommand(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	interfaces, err := ipv6.NewManager().List()
	if err != nil {
		return err
	}
	for _, item := range interfaces {
		state := "down"
		if item.Up {
			state = "up"
		}
		fmt.Printf("%s (%s, index=%d)\n", item.Name, state, item.Index)
		if item.Route != nil {
			fmt.Print("  default")
			if item.Route.Source.IsValid() {
				fmt.Printf(" source=%s", item.Route.Source)
			} else {
				fmt.Print(" source=(kernel-selected)")
			}
			if item.Route.Gateway.IsValid() {
				fmt.Printf(" gateway=%s", item.Route.Gateway)
			} else {
				fmt.Print(" gateway=(on-link)")
			}
			fmt.Printf(" onlink=%t\n", item.Route.OnLink)
		}
		for _, addr := range item.Addresses {
			fmt.Printf("  %s/%d scope=%s permanent=%t\n", addr.Addr, addr.PrefixLen, addr.Scope, addr.Permanent)
		}
	}
	return nil
}

func generateCommand(args []string) error {
	fs := flag.NewFlagSet("generate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	rawPrefix := fs.String("prefix", "", "IPv6 prefix")
	count := fs.Int("count", 1, "number of addresses")
	if err := fs.Parse(args); err != nil {
		return err
	}
	prefix, err := ipv6.ParsePrefix(*rawPrefix, 64)
	if err != nil {
		return err
	}
	addresses, err := ipv6.Generate(prefix, *count, nil)
	if err != nil {
		return err
	}
	for _, address := range addresses {
		fmt.Printf("%s/%d\n", address, prefix.Bits())
	}
	return nil
}

type commonFlags struct {
	iface  string
	state  string
	prefix string
	count  int
}

func parseCommon(fs *flag.FlagSet, flags *commonFlags) {
	fs.StringVar(&flags.iface, "iface", "", "network interface")
	fs.StringVar(&flags.state, "state", defaultState, "state file")
	fs.StringVar(&flags.prefix, "prefix", "", "IPv6 prefix used for generation")
	fs.IntVar(&flags.count, "count", 0, "number of generated addresses")
}

func parseAddresses(raw []string) ([]netip.Prefix, error) {
	result := make([]netip.Prefix, 0, len(raw))
	for _, value := range raw {
		prefix, err := ipv6.ParsePrefix(value, 64)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", value, err)
		}
		result = append(result, prefix)
	}
	return result, nil
}

func findInterface(manager ipv6.Manager, name string) (ipv6.Interface, error) {
	if strings.TrimSpace(name) == "" {
		return ipv6.Interface{}, errors.New("--iface is required")
	}
	items, err := manager.List()
	if err != nil {
		return ipv6.Interface{}, err
	}
	for _, item := range items {
		if item.Name == name {
			return item, nil
		}
	}
	return ipv6.Interface{}, fmt.Errorf("interface %q was not found", name)
}

func generatedAddresses(manager ipv6.Manager, flags commonFlags, item ipv6.Interface) ([]netip.Prefix, error) {
	if flags.prefix == "" || flags.count <= 0 {
		return nil, errors.New("provide addresses or both --prefix and a positive --count")
	}
	prefix, err := ipv6.ParsePrefix(flags.prefix, 64)
	if err != nil {
		return nil, err
	}
	exclude := make(map[netip.Addr]struct{}, len(item.Addresses))
	for _, address := range item.Addresses {
		exclude[address.Addr] = struct{}{}
	}
	addresses, err := ipv6.Generate(prefix, flags.count, exclude)
	if err != nil {
		return nil, err
	}
	result := make([]netip.Prefix, 0, len(addresses))
	for _, address := range addresses {
		result = append(result, netip.PrefixFrom(address, prefix.Bits()))
	}
	return result, nil
}

func addCommand(args []string) error {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var flags commonFlags
	parseCommon(fs, &flags)
	if err := fs.Parse(args); err != nil {
		return err
	}
	manager := ipv6.NewManager()
	item, err := findInterface(manager, flags.iface)
	if err != nil {
		return err
	}
	prefixes, err := parseAddresses(fs.Args())
	if err != nil {
		return err
	}
	if len(prefixes) == 0 {
		prefixes, err = generatedAddresses(manager, flags, item)
		if err != nil {
			return err
		}
	}
	added := make([]netip.Prefix, 0, len(prefixes))
	for _, prefix := range prefixes {
		err := manager.Add(flags.iface, prefix)
		if errors.Is(err, ipv6.ErrAddressExists) {
			fmt.Printf("skip %s (already present)\n", prefix)
			continue
		}
		if err != nil {
			for _, previous := range added {
				_ = manager.Delete(flags.iface, previous)
			}
			return fmt.Errorf("add %s failed; rolled back %d address(es): %w", prefix, len(added), err)
		}
		added = append(added, prefix)
		fmt.Printf("added %s on %s\n", prefix, flags.iface)
	}
	if err := ipv6.UpdateState(flags.state, func(state *ipv6.State) error {
		for _, prefix := range added {
			ipv6.Track(state, flags.iface, prefix)
		}
		return nil
	}); err != nil {
		for _, prefix := range added {
			_ = manager.Delete(flags.iface, prefix)
		}
		return fmt.Errorf("save state; rolled back %d address(es): %w", len(added), err)
	}
	return nil
}

func removeCommand(args []string) error {
	fs := flag.NewFlagSet("remove", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var flags commonFlags
	parseCommon(fs, &flags)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) == 0 {
		return errors.New("provide at least one address")
	}
	manager := ipv6.NewManager()
	if _, err := findInterface(manager, flags.iface); err != nil {
		return err
	}
	prefixes, err := parseAddresses(fs.Args())
	if err != nil {
		return err
	}
	removed := make([]netip.Prefix, 0, len(prefixes))
	var operationErr error
	for _, prefix := range prefixes {
		err := manager.Delete(flags.iface, prefix)
		if errors.Is(err, ipv6.ErrAddressMissing) {
			fmt.Printf("skip %s (not present)\n", prefix)
		} else if err != nil {
			operationErr = errors.Join(operationErr, fmt.Errorf("delete %s failed: %w", prefix, err))
			continue
		} else {
			fmt.Printf("removed %s from %s\n", prefix, flags.iface)
		}
		removed = append(removed, prefix)
	}
	if err := ipv6.UpdateState(flags.state, func(state *ipv6.State) error {
		for _, prefix := range removed {
			ipv6.Untrack(state, flags.iface, prefix)
		}
		return nil
	}); err != nil {
		operationErr = errors.Join(operationErr, fmt.Errorf("save state: %w", err))
	}
	return operationErr
}

func restoreCommand(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	statePath := fs.String("state", defaultState, "state file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	state, err := ipv6.LoadState(*statePath)
	if err != nil {
		return err
	}
	manager := ipv6.NewManager()
	for _, item := range state.Addresses {
		prefix, err := ipv6.ParsePrefix(item.Prefix, 64)
		if err != nil {
			return fmt.Errorf("state entry %s/%s: %w", item.Interface, item.Prefix, err)
		}
		err = manager.Add(item.Interface, prefix)
		if errors.Is(err, ipv6.ErrAddressExists) {
			fmt.Printf("present %s on %s\n", prefix, item.Interface)
			continue
		}
		if err != nil {
			return err
		}
		fmt.Printf("restored %s on %s\n", prefix, item.Interface)
	}
	for interfaceName, routeState := range state.DefaultRoutes {
		source, err := netip.ParseAddr(routeState.Source)
		if err != nil {
			return fmt.Errorf("route state %s: %w", interfaceName, err)
		}
		gateway := netip.Addr{}
		if routeState.Gateway != "" {
			gateway, err = netip.ParseAddr(routeState.Gateway)
			if err != nil {
				return fmt.Errorf("route gateway %s: %w", interfaceName, err)
			}
		}
		if err := manager.SetDefaultRoute(interfaceName, gateway, source, routeState.OnLink); err != nil {
			return fmt.Errorf("restore route on %s: %w", interfaceName, err)
		}
		fmt.Printf("restored default source %s on %s\n", source, interfaceName)
	}
	return nil
}

func routeSourceCommand(args []string) error {
	fs := flag.NewFlagSet("route-source", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	iface := fs.String("iface", "", "network interface")
	statePath := fs.String("state", defaultState, "state file")
	sourceRaw := fs.String("source", "", "IPv6 source address")
	gatewayRaw := fs.String("gateway", "", "IPv6 gateway; omitted for an on-link point-to-point route")
	onLink := fs.Bool("onlink", false, "mark the gateway as on-link")
	if err := fs.Parse(args); err != nil {
		return err
	}
	manager := ipv6.NewManager()
	item, err := findInterface(manager, *iface)
	if err != nil {
		return err
	}
	source, err := netip.ParseAddr(strings.TrimSpace(*sourceRaw))
	if err != nil || !source.Is6() || source.Is4In6() {
		return errors.New("--source must be a valid IPv6 address")
	}
	gateway := netip.Addr{}
	useOnLink := *onLink
	if strings.TrimSpace(*gatewayRaw) != "" {
		gateway, err = netip.ParseAddr(strings.TrimSpace(*gatewayRaw))
		if err != nil || !gateway.Is6() || gateway.Is4In6() {
			return errors.New("--gateway must be a valid IPv6 address")
		}
	} else if item.Route != nil {
		gateway = item.Route.Gateway
		useOnLink = item.Route.OnLink
	}
	if !gateway.IsValid() && !item.PointToPoint {
		return errors.New("no IPv6 gateway found; provide --gateway or use a point-to-point interface")
	}
	if err := manager.SetDefaultRoute(*iface, gateway, source, useOnLink); err != nil {
		return err
	}
	managed := ipv6.ManagedRoute{Source: source.String(), OnLink: useOnLink}
	if gateway.IsValid() {
		managed.Gateway = gateway.String()
	}
	if err := ipv6.UpdateState(*statePath, func(state *ipv6.State) error {
		if state.DefaultRoutes == nil {
			state.DefaultRoutes = map[string]ipv6.ManagedRoute{}
		}
		state.DefaultRoutes[*iface] = managed
		return nil
	}); err != nil {
		return fmt.Errorf("route changed but state save failed: %w", err)
	}
	fmt.Printf("default source set to %s on %s\n", source, *iface)
	return nil
}

func serveCommand(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	socketPath := fs.String("socket", "/run/ccore-ipv6/ccore-ipv6.sock", "local Unix socket")
	statePath := fs.String("state", defaultState, "state file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if runtime.GOOS != "linux" {
		return errors.New("serve currently supports Linux only")
	}
	if strings.TrimSpace(*socketPath) == "" {
		return errors.New("--socket is required")
	}
	if err := os.MkdirAll(filepath.Dir(*socketPath), 0o755); err != nil {
		return fmt.Errorf("create socket directory: %w", err)
	}
	if info, err := os.Stat(*socketPath); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("socket path exists and is not a Unix socket: %s", *socketPath)
		}
		conn, dialErr := net.DialTimeout("unix", *socketPath, 500*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			return fmt.Errorf("another ccore-ipv6 service is using %s", *socketPath)
		}
		if err := os.Remove(*socketPath); err != nil {
			return fmt.Errorf("remove stale socket: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect socket path: %w", err)
	}
	listener, err := net.Listen("unix", *socketPath)
	if err != nil {
		return fmt.Errorf("listen on Unix socket: %w", err)
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(*socketPath)
	}()
	if err := os.Chmod(*socketPath, 0o600); err != nil {
		return fmt.Errorf("protect Unix socket: %w", err)
	}
	service, err := api.New(ipv6.NewManager(), *statePath)
	if err != nil {
		return err
	}
	httpServer := &http.Server{
		Handler:           service.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       30 * time.Second,
	}
	errorsCh := make(chan error, 1)
	go func() { errorsCh <- httpServer.Serve(listener) }()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)
	select {
	case err := <-errorsCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case sig := <-stop:
		fmt.Fprintf(os.Stderr, "ccore-ipv6 serve stopping: %s\n", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(ctx); err != nil {
			_ = httpServer.Close()
			return err
		}
	}
	return nil
}
