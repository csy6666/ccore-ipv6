package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"

	"github.com/csy6666/ccore-ipv6/internal/ipv6"
)

type fakeManager struct {
	interfaces []ipv6.Interface
	added      []netip.Prefix
	deleted    []netip.Prefix
}

func (m *fakeManager) List() ([]ipv6.Interface, error) { return m.interfaces, nil }
func (m *fakeManager) Add(_ string, prefix netip.Prefix) error {
	m.added = append(m.added, prefix)
	return nil
}
func (m *fakeManager) Delete(_ string, prefix netip.Prefix) error {
	m.deleted = append(m.deleted, prefix)
	return nil
}
func (m *fakeManager) SetDefaultRoute(_ string, _ netip.Addr, _ netip.Addr, _ bool) error { return nil }
func (m *fakeManager) ClearDefaultRoute(_ string) error                                   { return nil }

func TestHandlerRejectsCrossOriginMutationAndUnknownFields(t *testing.T) {
	manager := &fakeManager{}
	service, err := New(manager, filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/addresses", nil)
	request.Header.Set("Origin", "https://attacker.example")
	response := httptest.NewRecorder()
	service.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status = %d, want %d", response.Code, http.StatusForbidden)
	}

	request = httptest.NewRequest(http.MethodPost, "/v1/addresses", strings.NewReader(`{"interface":"eth0","prefix":"2001:db8::/64","count":1,"extra":true}`))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	service.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d, want %d", response.Code, http.StatusBadRequest)
	}
}

func TestAddAddressPersistsOnlyAfterManagerAdds(t *testing.T) {
	manager := &fakeManager{interfaces: []ipv6.Interface{{
		Name: "eth0", Up: true,
		Addresses: []ipv6.Address{{Addr: netip.MustParseAddr("2001:db8:1::1"), PrefixLen: 64, Scope: "global"}},
	}}}
	statePath := filepath.Join(t.TempDir(), "state.json")
	service, err := New(manager, statePath)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/addresses", strings.NewReader(`{"interface":"eth0","prefix":"2001:db8:1::/64","count":1}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	service.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("add status = %d, body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Added []netip.Prefix `json:"added"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Added) != 1 || len(manager.added) != 1 {
		t.Fatalf("unexpected add response=%+v manager=%+v", payload, manager.added)
	}
	state, err := ipv6.LoadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Addresses) != 1 {
		t.Fatalf("state has %d addresses, want 1", len(state.Addresses))
	}
}
