package transport

import (
	"errors"
	"testing"
)

func TestEnforceStatusFilter_UnfilteredPasses(t *testing.T) {
	resp := &ListTenantsResponse{Tenants: []TenantInfo{
		{ID: "a", Status: TenantStatusActive},
		{ID: "b", Status: TenantStatusSuspended},
		{ID: "c"},
	}}
	for _, req := range []*ListTenantsRequest{nil, {}} {
		got, err := EnforceStatusFilter(req, resp)
		if err != nil {
			t.Fatalf("EnforceStatusFilter(%v): %v", req, err)
		}
		if got != resp {
			t.Fatalf("EnforceStatusFilter(%v) changed the response", req)
		}
	}
}

func TestEnforceStatusFilter_AllMatchPasses(t *testing.T) {
	req := &ListTenantsRequest{Status: TenantStatusSuspended}
	resp := &ListTenantsResponse{Tenants: []TenantInfo{
		{ID: "a", Status: TenantStatusSuspended},
		{ID: "b", Status: TenantStatusSuspended},
	}}
	got, err := EnforceStatusFilter(req, resp)
	if err != nil {
		t.Fatalf("EnforceStatusFilter: %v", err)
	}
	if got != resp {
		t.Fatal("EnforceStatusFilter changed a matching response")
	}
	empty := &ListTenantsResponse{}
	if got, err := EnforceStatusFilter(req, empty); err != nil || got != empty {
		t.Fatalf("EnforceStatusFilter(empty) = %v, %v; want the empty response", got, err)
	}
}

func TestEnforceStatusFilter_MismatchRefused(t *testing.T) {
	req := &ListTenantsRequest{Status: TenantStatusSuspended}
	resp := &ListTenantsResponse{Tenants: []TenantInfo{
		{ID: "a", Status: TenantStatusSuspended},
		{ID: "b", Status: TenantStatusActive},
	}}
	got, err := EnforceStatusFilter(req, resp)
	assertStatusFilterRefused(t, got, err)
}

func TestEnforceStatusFilter_EmptyTenantStatusRefused(t *testing.T) {
	req := &ListTenantsRequest{Status: TenantStatusActive}
	resp := &ListTenantsResponse{Tenants: []TenantInfo{{ID: "a"}}}
	got, err := EnforceStatusFilter(req, resp)
	assertStatusFilterRefused(t, got, err)
}

func assertStatusFilterRefused(t *testing.T, got *ListTenantsResponse, err error) {
	t.Helper()
	var cap *CapabilityUnsupportedError
	if !errors.As(err, &cap) {
		t.Fatalf("err = %v; want *CapabilityUnsupportedError", err)
	}
	if got != nil {
		t.Fatalf("response = %+v; want nil on refusal", got)
	}
}
