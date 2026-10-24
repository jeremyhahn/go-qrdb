package tenant

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func statusPtr(s Status) *Status { return &s }

// putStatusRecord writes a tenant record holding status (empty = a record
// written before the field existed).
func putStatusRecord(t *testing.T, put func(context.Context, string, []byte) error, id string, status Status) {
	t.Helper()
	field := ""
	if status != "" {
		field = `,"status":"` + string(status) + `"`
	}
	rec := []byte(`{"id":"` + id + `","name":"N","isolation_type":"prefix","created_at":"2026-01-02T03:04:05Z","updated_at":"2026-01-02T03:04:05Z"` + field + `}`)
	if err := put(context.Background(), metadataPrefix+id, rec); err != nil {
		t.Fatalf("put record: %v", err)
	}
}

func TestStatus_IsActiveFailsClosed(t *testing.T) {
	cases := map[Status]bool{
		StatusActive:    true,
		StatusSuspended: false,
		StatusDisabled:  false,
		"":              false,
		"Active":        false,
		"enabled":       false,
	}
	for s, want := range cases {
		if got := s.IsActive(); got != want {
			t.Errorf("Status(%q).IsActive() = %v, want %v", s, got, want)
		}
	}
	if Status("").Valid() || Status("enabled").Valid() {
		t.Fatal("empty or unknown status reported valid")
	}
	if StatusSuspended.String() != "suspended" {
		t.Fatalf("String() = %q", StatusSuspended.String())
	}
}

func TestStatus_CanTransitionTo(t *testing.T) {
	allowed := map[[2]Status]bool{
		{StatusActive, StatusActive}:       true,
		{StatusActive, StatusSuspended}:    true,
		{StatusActive, StatusDisabled}:     true,
		{StatusSuspended, StatusActive}:    true,
		{StatusSuspended, StatusSuspended}: true,
		{StatusSuspended, StatusDisabled}:  true,
		{StatusDisabled, StatusActive}:     true,
		{StatusDisabled, StatusDisabled}:   true,
		{StatusDisabled, StatusSuspended}:  false,
	}
	for pair, want := range allowed {
		if got := pair[0].CanTransitionTo(pair[1]); got != want {
			t.Errorf("%q -> %q = %v, want %v", pair[0], pair[1], got, want)
		}
	}
	for _, from := range append(knownStatuses(), "", "bogus") {
		if from.CanTransitionTo("bogus") || from.CanTransitionTo("") {
			t.Errorf("%q may move to an unknown status", from)
		}
	}
	if !Status("").CanTransitionTo(StatusActive) || !Status("bogus").CanTransitionTo(StatusDisabled) {
		t.Fatal("a record with no known status cannot be repaired to a known one")
	}
}

func TestUpdateTenant_StatusTransitions(t *testing.T) {
	for _, fx := range descriptiveFixtures {
		t.Run(fx.name, func(t *testing.T) {
			svc, put := fx.new(t)
			ctx := context.Background()
			for _, from := range knownStatuses() {
				for _, to := range knownStatuses() {
					id := "t-" + string(from) + "-" + string(to)
					putStatusRecord(t, put, id, from)
					got, err := svc.UpdateTenant(ctx, id, TenantPatch{Status: statusPtr(to)})
					if from.CanTransitionTo(to) {
						if err != nil || got.Status != to {
							t.Fatalf("%s -> %s: got %+v, %v", from, to, got, err)
						}
						continue
					}
					var te *StatusTransitionError
					if !errors.As(err, &te) || te.From != from || te.To != to || te.TenantID != id {
						t.Fatalf("%s -> %s: err = %T(%v), want *StatusTransitionError", from, to, err, err)
					}
					if !strings.Contains(te.Error(), "cannot transition tenant "+id) {
						t.Fatalf("message = %q", te.Error())
					}
					stored, _ := svc.GetTenant(ctx, id)
					if stored.Status != from {
						t.Fatalf("%s -> %s refused but stored status = %q", from, to, stored.Status)
					}
				}
			}
		})
	}
}

// Both create sites store a new tenant active, and the stored record reads
// back active.
func TestCreateTenant_StoresActive(t *testing.T) {
	for _, fx := range descriptiveFixtures {
		t.Run(fx.name, func(t *testing.T) {
			svc, _ := fx.new(t)
			ctx := context.Background()
			created, err := svc.CreateTenant(ctx, "t-new", "New", IsolationPrefix)
			if err != nil || created.Status != StatusActive {
				t.Fatalf("CreateTenant = %+v, %v; want status active", created, err)
			}
			stored, err := svc.GetTenant(ctx, "t-new")
			if err != nil || !stored.Status.IsActive() {
				t.Fatalf("stored = %+v, %v; want status active", stored, err)
			}
		})
	}
}

func TestUpdateTenant_StatusUnknownRefused(t *testing.T) {
	for _, fx := range descriptiveFixtures {
		t.Run(fx.name, func(t *testing.T) {
			svc, put := fx.new(t)
			putStatusRecord(t, put, "t-1", StatusActive)
			_, err := svc.UpdateTenant(context.Background(), "t-1", TenantPatch{Status: statusPtr("paused")})
			var ie *InvalidStatusError
			if !errors.As(err, &ie) || ie.Value != "paused" {
				t.Fatalf("err = %T(%v), want *InvalidStatusError", err, err)
			}
			if !strings.Contains(ie.Error(), "allowed: active, suspended, disabled") {
				t.Fatalf("message = %q", ie.Error())
			}
		})
	}
}

func TestUpdateTenant_StatusAbsentUnchanged(t *testing.T) {
	for _, fx := range descriptiveFixtures {
		t.Run(fx.name, func(t *testing.T) {
			svc, put := fx.new(t)
			ctx := context.Background()
			putStatusRecord(t, put, "t-s", StatusSuspended)
			got, err := svc.UpdateTenant(ctx, "t-s", TenantPatch{Description: strPtr("d")})
			if err != nil || got.Status != StatusSuspended {
				t.Fatalf("absent status changed it: %+v, %v", got, err)
			}
			putStatusRecord(t, put, "t-old", "")
			old, err := svc.GetTenant(ctx, "t-old")
			if err != nil || old.Status.IsActive() {
				t.Fatalf("pre-status record reads active: %+v, %v", old, err)
			}
			got, err = svc.UpdateTenant(ctx, "t-old", TenantPatch{Status: statusPtr(StatusActive)})
			if err != nil || !got.Status.IsActive() {
				t.Fatalf("reactivating a pre-status record: %+v, %v", got, err)
			}
		})
	}
}
