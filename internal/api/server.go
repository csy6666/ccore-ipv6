package api

import (
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
	"strings"
	"sync"

	"github.com/csy6666/ccore-ipv6/internal/ipv6"
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
