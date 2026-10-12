package api

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/csy6666/ccore-ipv6/internal/dns/cloudflare"
	"github.com/csy6666/ccore-ipv6/internal/ipv6"
	"github.com/csy6666/ccore-ipv6/internal/probe"
)

//go:embed panel/index.html panel/app.js panel/style.css panel/favicon.svg
var panelFiles embed.FS

type Service struct {
	manager   ipv6.Manager
	statePath string
	mu        sync.Mutex
}

func New(manager ipv6.Manager, statePath string) (*Service, error) {
	if manager == nil {
		return nil, errors.New("network manager is required")
	}
	if strings.TrimSpace(statePath) == "" {
		return nil, errors.New("state path is required")
	}
	return &Service{manager: manager, statePath: statePath}, nil
}

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /v1/interfaces", s.interfaces)
	mux.HandleFunc("GET /v1/state", s.state)
	mux.HandleFunc("POST /v1/addresses", s.addAddresses)
	mux.HandleFunc("DELETE /v1/addresses", s.removeAddress)
	mux.HandleFunc("POST /v1/probes", s.probes)
	mux.HandleFunc("POST /v1/routes/source", s.setRouteSource)
	mux.HandleFunc("GET /v1/dns/zones", s.dnsZones)
	mux.HandleFunc("POST /v1/dns/preview", s.dnsPreview)
	mux.HandleFunc("POST /v1/dns/sync", s.dnsSync)
	mux.HandleFunc("POST /v1/restore", s.restore)
	panel, err := fs.Sub(panelFiles, "panel")
	if err == nil {
		mux.Handle("GET /", http.FileServer(http.FS(panel)))
	}
	return withHeaders(mux)
}

func withHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		if r.Method == http.MethodPost || r.Method == http.MethodDelete {
			if origin := r.Header.Get("Origin"); origin != "" {
				parsed, err := url.Parse(origin)
				if err != nil || parsed.Host != r.Host || (parsed.Scheme != "http" && parsed.Scheme != "https") {
					writeError(w, http.StatusForbidden, errors.New("cross-origin mutation is not allowed"))
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

type addAddressesRequest struct {
	Interface string `json:"interface"`
	Prefix    string `json:"prefix"`
	Count     int    `json:"count"`
}

type dnsSyncRequest struct {
	ZoneID   string `json:"zone_id"`
	ZoneName string `json:"zone_name"`
	Name     string `json:"name"`
	TTL      int    `json:"ttl"`
	Proxied  bool   `json:"proxied"`
}

type dnsPreviewItem struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	Exists  bool   `json:"exists"`
}

type dnsPreviewResponse struct {
	Zone     cloudflare.Zone  `json:"zone"`
	Items    []dnsPreviewItem `json:"items"`
	Missing  int              `json:"missing"`
	Existing int              `json:"existing"`
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return errors.New("content type must be application/json")
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			return errors.New("request must contain one JSON value")
		}
		return err
	}
	return nil
}

func (s *Service) addAddresses(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var request addAddressesRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	request.Interface = strings.TrimSpace(request.Interface)
	if request.Interface == "" {
		writeError(w, http.StatusBadRequest, errors.New("interface is required"))
		return
	}
	if request.Count < 1 || request.Count > 64 {
		writeError(w, http.StatusBadRequest, errors.New("count must be between 1 and 64"))
		return
	}
	prefix, err := ipv6.ParsePrefix(request.Prefix, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	interfaces, err := s.manager.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	exclude := make(map[netip.Addr]struct{})
	selected := false
	prefixOnLink := false
	for _, item := range interfaces {
		if item.Name == request.Interface {
			selected = item.Up
			for _, address := range item.Addresses {
				if address.Scope == "global" && address.PrefixLen <= prefix.Bits() && address.Prefix().Contains(prefix.Addr()) {
					prefixOnLink = true
				}
			}
		}
		for _, address := range item.Addresses {
			exclude[address.Addr] = struct{}{}
		}
	}
	if !selected {
		writeError(w, http.StatusBadRequest, errors.New("interface was not found or is down"))
		return
	}
	if !prefixOnLink {
		writeError(w, http.StatusBadRequest, errors.New("prefix must be inside a global IPv6 prefix on the selected interface"))
		return
	}
	state, err := ipv6.LoadState(s.statePath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	for _, item := range state.Addresses {
		managed, parseErr := ipv6.ParsePrefix(item.Prefix, 64)
		if parseErr == nil {
			exclude[managed.Addr()] = struct{}{}
		}
	}
	addresses, err := ipv6.Generate(prefix, request.Count, exclude)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	added := make([]netip.Prefix, 0, len(addresses))
	for _, address := range addresses {
		item := netip.PrefixFrom(address, prefix.Bits())
		if err := s.manager.Add(request.Interface, item); err != nil {
			for _, rollback := range added {
				_ = s.manager.Delete(request.Interface, rollback)
			}
			writeError(w, http.StatusConflict, fmt.Errorf("add %s failed; rolled back %d address(es): %w", item, len(added), err))
			return
		}
		added = append(added, item)
	}
	if err := ipv6.UpdateState(s.statePath, func(state *ipv6.State) error {
		for _, item := range added {
			ipv6.Track(state, request.Interface, item)
		}
		return nil
	}); err != nil {
		for _, item := range added {
			_ = s.manager.Delete(request.Interface, item)
		}
		writeError(w, http.StatusInternalServerError, fmt.Errorf("save state; rolled back %d address(es): %w", len(added), err))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"added": added})
}

type removeAddressRequest struct {
	Interface string `json:"interface"`
	Prefix    string `json:"prefix"`
}

func (s *Service) removeAddress(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var request removeAddressRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	request.Interface = strings.TrimSpace(request.Interface)
	prefix, err := ipv6.ParsePrefix(request.Prefix, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	tracked := false
	checkedState := false
	if err := ipv6.UpdateState(s.statePath, func(state *ipv6.State) error {
		checkedState = true
		for _, item := range state.Addresses {
			if item.Interface == request.Interface && item.Prefix == prefix.String() {
				tracked = true
				break
			}
		}
		if !tracked {
			return errors.New("address is not managed by ccore-ipv6")
		}
		err := s.manager.Delete(request.Interface, prefix)
		if err != nil && !errors.Is(err, ipv6.ErrAddressMissing) {
			return err
		}
		ipv6.Untrack(state, request.Interface, prefix)
		return nil
	}); err != nil {
		status := http.StatusConflict
		if checkedState && !tracked {
			status = http.StatusForbidden
		} else if !checkedState {
			status = http.StatusInternalServerError
		}
		writeError(w, status, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func cloudflareClient() (*cloudflare.Client, error) {
	return cloudflare.New(strings.TrimSpace(os.Getenv("CCORE_IPV6_CF_TOKEN")))
}

func (s *Service) dnsZones(w http.ResponseWriter, r *http.Request) {
	client, err := cloudflareClient()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	zones, err := client.ListZones(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"zones": zones})
}

func (s *Service) dnsPreview(w http.ResponseWriter, r *http.Request) {
	request, _, zone, names, addresses, err := s.prepareDNS(w, r)
	if err != nil {
		writeError(w, dnsErrorStatus(err), err)
		return
	}
	client, err := cloudflareClient()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	existing, err := client.ListAAAA(ctx, zone.ID)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	have := make(map[string]struct{}, len(existing))
	for _, record := range existing {
		have[dnsRecordKey(record.Name, record.Content)] = struct{}{}
	}
	items := make([]dnsPreviewItem, 0, len(names))
	response := dnsPreviewResponse{Zone: zone, Items: items}
	for index, name := range names {
		item := dnsPreviewItem{Name: name, Address: addresses[index].String()}
		_, item.Exists = have[dnsRecordKey(name, item.Address)]
		if item.Exists {
			response.Existing++
		} else {
			response.Missing++
		}
		response.Items = append(response.Items, item)
	}
	_ = request
	writeJSON(w, http.StatusOK, response)
}

func (s *Service) dnsSync(w http.ResponseWriter, r *http.Request) {
	request, _, zone, names, addresses, err := s.prepareDNS(w, r)
	if err != nil {
		writeError(w, dnsErrorStatus(err), err)
		return
	}
	client, err := cloudflareClient()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	result, err := client.SyncAAAA(ctx, zone.ID, names, addresses, request.TTL, request.Proxied)
	response := map[string]any{
		"zone":    zone,
		"created": result.Created,
		"skipped": result.Skipped,
		"failed":  result.Failed,
	}
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "created": result.Created, "skipped": result.Skipped, "failed": result.Failed})
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Service) prepareDNS(w http.ResponseWriter, r *http.Request) (dnsSyncRequest, ipv6.State, cloudflare.Zone, []string, []netip.Addr, error) {
	var request dnsSyncRequest
	if err := decodeJSON(w, r, &request); err != nil {
		return request, ipv6.State{}, cloudflare.Zone{}, nil, nil, err
	}
	request.ZoneID = strings.TrimSpace(request.ZoneID)
	request.ZoneName = strings.ToLower(strings.Trim(strings.TrimSpace(request.ZoneName), "."))
	request.Name = strings.TrimSpace(request.Name)
	if request.ZoneID == "" || request.ZoneName == "" {
		return request, ipv6.State{}, cloudflare.Zone{}, nil, nil, errors.New("zone_id and zone_name are required")
	}
	if request.TTL <= 0 {
		request.TTL = 60
	}
	if request.TTL > 86400 {
		return request, ipv6.State{}, cloudflare.Zone{}, nil, nil, errors.New("ttl must be between 1 and 86400")
	}
	state, err := ipv6.LoadState(s.statePath)
	if err != nil {
		return request, state, cloudflare.Zone{}, nil, nil, err
	}
	if len(state.Addresses) == 0 {
		return request, state, cloudflare.Zone{}, nil, nil, errors.New("state has no managed addresses")
	}
	addresses := make([]netip.Addr, 0, len(state.Addresses))
	for _, item := range state.Addresses {
		prefix, parseErr := ipv6.ParsePrefix(item.Prefix, 64)
		if parseErr != nil {
			return request, state, cloudflare.Zone{}, nil, nil, fmt.Errorf("state entry %s/%s: %w", item.Interface, item.Prefix, parseErr)
		}
		addresses = append(addresses, prefix.Addr())
	}
	names, err := cloudflare.ExpandNames(request.Name, request.ZoneName, len(addresses))
	if err != nil {
		return request, state, cloudflare.Zone{}, nil, nil, err
	}
	client, err := cloudflareClient()
	if err != nil {
		return request, state, cloudflare.Zone{}, nil, nil, err
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	zones, err := client.ListZones(ctx)
	if err != nil {
		return request, state, cloudflare.Zone{}, nil, nil, err
	}
	for _, zone := range zones {
		if zone.ID == request.ZoneID && strings.EqualFold(strings.TrimSuffix(zone.Name, "."), request.ZoneName) {
			return request, state, zone, names, addresses, nil
		}
	}
	return request, state, cloudflare.Zone{}, nil, nil, errors.New("zone_id and zone_name do not match an accessible Cloudflare zone")
}

func dnsRecordKey(name, content string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), ".")) + "|" + strings.ToLower(strings.TrimSpace(content))
}

func dnsErrorStatus(err error) int {
	if strings.Contains(err.Error(), "Cloudflare") {
		return http.StatusBadGateway
	}
	if strings.Contains(err.Error(), "token") {
		return http.StatusServiceUnavailable
	}
	if strings.Contains(err.Error(), "state") {
		return http.StatusConflict
	}
	return http.StatusBadRequest
}

type probeResponse struct {
	Results []probe.Result `json:"results"`
	Passed  int            `json:"passed"`
	TCPOnly int            `json:"tcp_only"`
	Failed  int            `json:"failed"`
}

func (s *Service) probes(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	state, err := ipv6.LoadState(s.statePath)
	s.mu.Unlock()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if len(state.Addresses) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("state has no managed addresses"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	results := make([]probe.Result, len(state.Addresses))
	workers := 4
	if len(results) < workers {
		workers = len(results)
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				item := state.Addresses[index]
				prefix, parseErr := ipv6.ParsePrefix(item.Prefix, 64)
				if parseErr != nil {
					results[index] = probe.Result{
						Interface: item.Interface,
						Prefix:    item.Prefix,
						CheckedAt: time.Now().UTC(),
						Error:     parseErr.Error(),
					}
					continue
				}
				results[index] = probe.Run(ctx, item.Interface, item.Prefix, prefix.Addr())
			}
		}()
	}
sendJobs:
	for index := range state.Addresses {
		select {
		case jobs <- index:
		case <-ctx.Done():
			break sendJobs
		}
	}
	close(jobs)
	wg.Wait()
	response := probeResponse{Results: results}
	for _, result := range results {
		switch {
		case result.HTTP:
			response.Passed++
		case result.TCP:
			response.TCPOnly++
		default:
			response.Failed++
		}
	}
	writeJSON(w, http.StatusOK, response)
}

type routeSourceRequest struct {
	Interface string `json:"interface"`
	Source    string `json:"source"`
}

type routeSourceResponse struct {
	Interface string      `json:"interface"`
	Mode      string      `json:"mode"`
	Route     *ipv6.Route `json:"route,omitempty"`
}

func (s *Service) setRouteSource(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var request routeSourceRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	request.Interface = strings.TrimSpace(request.Interface)
	if request.Interface == "" {
		writeError(w, http.StatusBadRequest, errors.New("interface is required"))
		return
	}
	interfaces, err := s.manager.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	var selected *ipv6.Interface
	for index := range interfaces {
		if interfaces[index].Name == request.Interface {
			selected = &interfaces[index]
			break
		}
	}
	if selected == nil || !selected.Up {
		writeError(w, http.StatusBadRequest, errors.New("interface was not found or is down"))
		return
	}
	if selected.Route == nil {
		writeError(w, http.StatusBadRequest, errors.New("interface has no IPv6 default route"))
		return
	}
	state, err := ipv6.LoadState(s.statePath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	sourceRaw := strings.TrimSpace(request.Source)
	if sourceRaw == "" {
		if _, managedRoute := state.DefaultRoutes[request.Interface]; !managedRoute {
			writeError(w, http.StatusForbidden, errors.New("default route is not managed by ccore-ipv6"))
			return
		}
		if err := s.manager.ClearDefaultRoute(request.Interface); err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		delete(state.DefaultRoutes, request.Interface)
		if err := ipv6.SaveState(s.statePath, state); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("route changed but state save failed: %w", err))
			return
		}
		writeJSON(w, http.StatusOK, routeSourceResponse{Interface: request.Interface, Mode: "automatic", Route: selected.Route})
		return
	}
	source, err := netip.ParseAddr(sourceRaw)
	if err != nil || !source.Is6() || source.Is4In6() {
		writeError(w, http.StatusBadRequest, errors.New("source must be a valid IPv6 address"))
		return
	}
	managed := false
	for _, item := range state.Addresses {
		prefix, parseErr := ipv6.ParsePrefix(item.Prefix, 64)
		if parseErr == nil && item.Interface == request.Interface && prefix.Addr() == source {
			managed = true
			break
		}
	}
	if !managed {
		writeError(w, http.StatusForbidden, errors.New("source address is not managed by ccore-ipv6"))
		return
	}
	present := false
	for _, item := range selected.Addresses {
		if item.Addr == source {
			present = true
			break
		}
	}
	if !present {
		writeError(w, http.StatusConflict, errors.New("source address is not present on the selected interface"))
		return
	}
	if err := s.manager.SetDefaultRoute(request.Interface, selected.Route.Gateway, source, selected.Route.OnLink); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	if state.DefaultRoutes == nil {
		state.DefaultRoutes = map[string]ipv6.ManagedRoute{}
	}
	managedRoute := ipv6.ManagedRoute{Source: source.String(), OnLink: selected.Route.OnLink}
	if selected.Route.Gateway.IsValid() {
		managedRoute.Gateway = selected.Route.Gateway.String()
	}
	state.DefaultRoutes[request.Interface] = managedRoute
	if err := ipv6.SaveState(s.statePath, state); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("route changed but state save failed: %w", err))
		return
	}
	updatedRoute := *selected.Route
	updatedRoute.Source = source
	writeJSON(w, http.StatusOK, routeSourceResponse{Interface: request.Interface, Mode: "managed", Route: &updatedRoute})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func (s *Service) interfaces(w http.ResponseWriter, _ *http.Request) {
	items, err := s.manager.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

type stateResponse struct {
	State      ipv6.State       `json:"state"`
	Interfaces []ipv6.Interface `json:"interfaces"`
}

func (s *Service) state(w http.ResponseWriter, _ *http.Request) {
	state, err := ipv6.LoadState(s.statePath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	items, err := s.manager.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, stateResponse{State: state, Interfaces: items})
}

type restoreItem struct {
	Target string `json:"target"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type restoreResponse struct {
	Items    []restoreItem `json:"items"`
	Restored int           `json:"restored"`
	Present  int           `json:"present"`
	Failed   int           `json:"failed"`
}

func (s *Service) restore(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := ipv6.LoadState(s.statePath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	result := restoreResponse{}
	for _, item := range state.Addresses {
		target := item.Interface + " " + item.Prefix
		prefix, err := ipv6.ParsePrefix(item.Prefix, 64)
		if err != nil {
			result.Failed++
			result.Items = append(result.Items, restoreItem{Target: target, Status: "failed", Error: err.Error()})
			continue
		}
		err = s.manager.Add(item.Interface, prefix)
		if errors.Is(err, ipv6.ErrAddressExists) {
			result.Present++
			result.Items = append(result.Items, restoreItem{Target: target, Status: "present"})
			continue
		}
		if err != nil {
			result.Failed++
			result.Items = append(result.Items, restoreItem{Target: target, Status: "failed", Error: err.Error()})
			continue
		}
		result.Restored++
		result.Items = append(result.Items, restoreItem{Target: target, Status: "restored"})
	}
	for interfaceName, routeState := range state.DefaultRoutes {
		target := interfaceName + " route-source " + routeState.Source
		source, parseErr := netip.ParseAddr(routeState.Source)
		if parseErr != nil {
			result.Failed++
			result.Items = append(result.Items, restoreItem{Target: target, Status: "failed", Error: parseErr.Error()})
			continue
		}
		gateway := netip.Addr{}
		if routeState.Gateway != "" {
			gateway, parseErr = netip.ParseAddr(routeState.Gateway)
			if parseErr != nil {
				result.Failed++
				result.Items = append(result.Items, restoreItem{Target: target, Status: "failed", Error: parseErr.Error()})
				continue
			}
		}
		if err := s.manager.SetDefaultRoute(interfaceName, gateway, source, routeState.OnLink); err != nil {
			result.Failed++
			result.Items = append(result.Items, restoreItem{Target: target, Status: "failed", Error: err.Error()})
			continue
		}
		result.Restored++
		result.Items = append(result.Items, restoreItem{Target: target, Status: "restored"})
	}
	status := http.StatusOK
	if result.Failed > 0 {
		status = http.StatusMultiStatus
	}
	writeJSON(w, status, result)
}

func (s *Service) String() string {
	return fmt.Sprintf("ccore-ipv6 api state=%s", s.statePath)
}
