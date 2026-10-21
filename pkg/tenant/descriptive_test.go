package tenant

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"strconv"
	"strings"
	"testing"
)

// descriptiveFixture is one TenantServicer implementation plus a raw writer
// into the store it persists tenant records in.
type descriptiveFixture struct {
	name string
	new  func(t *testing.T) (TenantServicer, func(ctx context.Context, key string, value []byte) error)
}

var descriptiveFixtures = []descriptiveFixture{
	{name: "TenantService", new: func(t *testing.T) (TenantServicer, func(context.Context, string, []byte) error) {
		svc, _ := newTestService(t)
		return svc, svc.store.Put
	}},
	{name: "kvTenantService", new: func(t *testing.T) (TenantServicer, func(context.Context, string, []byte) error) {
		store := newMemKVStore()
		svc, err := NewKVTenantService(store, slog.Default())
		if err != nil {
			t.Fatalf("NewKVTenantService: %v", err)
		}
		return svc, store.Put
	}},
}

func strPtr(s string) *string { return &s }

func TestUpdateTenant_DescriptiveSetReplaceClear(t *testing.T) {
	for _, fx := range descriptiveFixtures {
		t.Run(fx.name, func(t *testing.T) {
			svc, _ := fx.new(t)
			ctx := context.Background()
			if _, err := svc.CreateTenant(ctx, "t-desc", "Desc", ""); err != nil {
				t.Fatalf("CreateTenant: %v", err)
			}

			set := map[string]string{"env": "prod", "team": "payments"}
			got, err := svc.UpdateTenant(ctx, "t-desc", TenantPatch{Description: strPtr("billing tenant"), Metadata: set})
			if err != nil {
				t.Fatalf("UpdateTenant set: %v", err)
			}
			if got.Description != "billing tenant" || !maps.Equal(got.Metadata, set) {
				t.Fatalf("after set: description=%q metadata=%v", got.Description, got.Metadata)
			}
			set["env"] = "mutated"
			if got.Metadata["env"] != "prod" {
				t.Fatal("record aliases the caller's patch map")
			}

			// A patch that sets neither leaves both unchanged.
			got, err = svc.UpdateTenant(ctx, "t-desc", TenantPatch{Name: "Renamed"})
			if err != nil {
				t.Fatalf("UpdateTenant rename: %v", err)
			}
			if got.Description != "billing tenant" || len(got.Metadata) != 2 {
				t.Fatalf("rename changed descriptive fields: %q %v", got.Description, got.Metadata)
			}

			// Metadata replaces the whole map.
			replaced := map[string]string{"owner": "ops"}
			if _, err = svc.UpdateTenant(ctx, "t-desc", TenantPatch{Metadata: replaced}); err != nil {
				t.Fatalf("UpdateTenant replace: %v", err)
			}
			persisted, err := svc.GetTenant(ctx, "t-desc")
			if err != nil {
				t.Fatalf("GetTenant: %v", err)
			}
			if !maps.Equal(persisted.Metadata, replaced) || persisted.Description != "billing tenant" {
				t.Fatalf("after replace: description=%q metadata=%v", persisted.Description, persisted.Metadata)
			}

			// An empty map and an empty description clear.
			if _, err = svc.UpdateTenant(ctx, "t-desc", TenantPatch{Description: strPtr(""), Metadata: map[string]string{}}); err != nil {
				t.Fatalf("UpdateTenant clear: %v", err)
			}
			persisted, err = svc.GetTenant(ctx, "t-desc")
			if err != nil {
				t.Fatalf("GetTenant: %v", err)
			}
			if persisted.Description != "" || persisted.Metadata != nil {
				t.Fatalf("after clear: description=%q metadata=%v", persisted.Description, persisted.Metadata)
			}
		})
	}
}

func TestUpdateTenant_DescriptiveRecordWithoutFieldsDecodes(t *testing.T) {
	for _, fx := range descriptiveFixtures {
		t.Run(fx.name, func(t *testing.T) {
			svc, put := fx.new(t)
			ctx := context.Background()
			old := []byte(`{"id":"t-old","name":"Old","isolation_type":"prefix","created_at":"2026-01-02T03:04:05Z","updated_at":"2026-01-02T03:04:05Z"}`)
			if err := put(ctx, metadataPrefix+"t-old", old); err != nil {
				t.Fatalf("put old record: %v", err)
			}
			info, err := svc.GetTenant(ctx, "t-old")
			if err != nil {
				t.Fatalf("GetTenant: %v", err)
			}
			if info.Name != "Old" || info.Description != "" || info.Metadata != nil {
				t.Fatalf("old record decoded as name=%q description=%q metadata=%v", info.Name, info.Description, info.Metadata)
			}
			updated, err := svc.UpdateTenant(ctx, "t-old", TenantPatch{Metadata: map[string]string{"k": "v"}})
			if err != nil {
				t.Fatalf("UpdateTenant: %v", err)
			}
			if updated.Name != "Old" || updated.Metadata["k"] != "v" {
				t.Fatalf("update of old record: %+v", updated)
			}
		})
	}
}

func TestUpdateTenant_DescriptiveLimitsRefused(t *testing.T) {
	tooMany := make(map[string]string, MaxMetadataEntries+1)
	for i := range MaxMetadataEntries + 1 {
		tooMany["k"+strconv.Itoa(i)] = "v"
	}
	cases := []struct {
		name  string
		patch TenantPatch
		field string
	}{
		{"description too long", TenantPatch{Description: strPtr(strings.Repeat("d", MaxDescriptionBytes+1))}, "description"},
		{"description not utf8", TenantPatch{Description: strPtr("\xff")}, "description"},
		{"too many entries", TenantPatch{Metadata: tooMany}, "metadata"},
		{"empty key", TenantPatch{Metadata: map[string]string{"": "v"}}, "metadata key"},
		{"key too long", TenantPatch{Metadata: map[string]string{strings.Repeat("k", MaxMetadataKeyBytes+1): "v"}}, "metadata key"},
		{"value too long", TenantPatch{Metadata: map[string]string{"k": strings.Repeat("v", MaxMetadataValueBytes+1)}}, "metadata value"},
		{"value not utf8", TenantPatch{Metadata: map[string]string{"k": "\xc3"}}, "metadata value"},
	}
	for _, fx := range descriptiveFixtures {
		t.Run(fx.name, func(t *testing.T) {
			svc, _ := fx.new(t)
			ctx := context.Background()
			if _, err := svc.CreateTenant(ctx, "t-lim", "Lim", ""); err != nil {
				t.Fatalf("CreateTenant: %v", err)
			}
			for _, tc := range cases {
				_, err := svc.UpdateTenant(ctx, "t-lim", tc.patch)
				var fe *TenantFieldError
				if !errors.As(err, &fe) || fe.Field != tc.field {
					t.Errorf("%s: err = %v, want *TenantFieldError{Field: %q}", tc.name, err, tc.field)
				}
			}
			got, err := svc.GetTenant(ctx, "t-lim")
			if err != nil {
				t.Fatalf("GetTenant: %v", err)
			}
			if got.Description != "" || got.Metadata != nil {
				t.Fatalf("refused patch persisted: %q %v", got.Description, got.Metadata)
			}
		})
	}
}

func TestUpdateTenant_DescriptiveAtLimitsAccepted(t *testing.T) {
	full := make(map[string]string, MaxMetadataEntries)
	for i := range MaxMetadataEntries {
		full[strings.Repeat("k", MaxMetadataKeyBytes-3)+strconv.Itoa(100+i)] = strings.Repeat("v", MaxMetadataValueBytes)
	}
	svc, _ := newTestService(t)
	ctx := context.Background()
	if _, err := svc.CreateTenant(ctx, "t-max", "Max", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	got, err := svc.UpdateTenant(ctx, "t-max", TenantPatch{Description: strPtr(strings.Repeat("d", MaxDescriptionBytes)), Metadata: full})
	if err != nil {
		t.Fatalf("UpdateTenant at the limits: %v", err)
	}
	if len(got.Metadata) != MaxMetadataEntries || len(got.Description) != MaxDescriptionBytes {
		t.Fatalf("at-limit update stored %d entries, %d description bytes", len(got.Metadata), len(got.Description))
	}
}

func TestTenantFieldError_Error(t *testing.T) {
	err := &TenantFieldError{Field: "metadata key", Reason: "empty"}
	if got, want := err.Error(), "tenant: invalid metadata key: empty"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}
