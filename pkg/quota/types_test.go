// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package quota

import (
	"strings"
	"testing"
)

// --- Resource.String() ---

func TestResourceStringKnownValues(t *testing.T) {
	tests := []struct {
		resource Resource
		want     string
	}{
		{ResourceDatabases, "databases"},
		{ResourceRaftGroups, "raft_groups"},
		{ResourceCollections, "collections"},
		{ResourceKeys, "keys"},
		{ResourceStorageBytes, "storage_bytes"},
		{ResourceUsers, "users"},
		{ResourceRoles, "roles"},
		{ResourceCertificates, "certificates"},
		{ResourceBackups, "backups"},
		{ResourceVectorDims, "vector_dims"},
		{ResourceTSMetrics, "ts_metrics"},
	}
	for _, tc := range tests {
		got := tc.resource.String()
		if got != tc.want {
			t.Errorf("Resource(%d).String() = %q, want %q", int(tc.resource), got, tc.want)
		}
	}
}

func TestResourceStringUnknownValue(t *testing.T) {
	r := Resource(999)
	got := r.String()
	if !strings.Contains(got, "unknown") {
		t.Errorf("Resource(999).String() = %q, want it to contain %q", got, "unknown")
	}
	if !strings.Contains(got, "999") {
		t.Errorf("Resource(999).String() = %q, want it to contain %q", got, "999")
	}
}

// --- Quotas.Limit() ---

func TestQuotasLimitReturnsCorrectFieldForEachResource(t *testing.T) {
	q := &Quotas{
		MaxDatabases:    1,
		MaxRaftGroups:   2,
		MaxCollections:  3,
		MaxKeys:         4,
		MaxStorageBytes: 5,
		MaxUsers:        6,
		MaxRoles:        7,
		MaxCertificates: 8,
		MaxBackups:      9,
		MaxVectorDims:   10,
		MaxTSMetrics:    11,
	}
	tests := []struct {
		resource Resource
		want     int64
	}{
		{ResourceDatabases, 1},
		{ResourceRaftGroups, 2},
		{ResourceCollections, 3},
		{ResourceKeys, 4},
		{ResourceStorageBytes, 5},
		{ResourceUsers, 6},
		{ResourceRoles, 7},
		{ResourceCertificates, 8},
		{ResourceBackups, 9},
		{ResourceVectorDims, 10},
		{ResourceTSMetrics, 11},
	}
	for _, tc := range tests {
		got, err := q.Limit(tc.resource)
		if err != nil {
			t.Errorf("Quotas.Limit(%v) returned unexpected error: %v", tc.resource, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Quotas.Limit(%v) = %d, want %d", tc.resource, got, tc.want)
		}
	}
}

func TestQuotasLimitUnknownResourceReturnsError(t *testing.T) {
	q := &Quotas{}
	_, err := q.Limit(Resource(999))
	if err == nil {
		t.Fatal("Quotas.Limit(unknown) expected error, got nil")
	}
	if _, ok := err.(*InvalidResourceError); !ok {
		t.Errorf("Quotas.Limit(unknown) error type = %T, want *InvalidResourceError", err)
	}
}

func TestQuotasZeroValueLimitReturnsZero(t *testing.T) {
	q := &Quotas{} // all fields are zero
	got, err := q.Limit(ResourceKeys)
	if err != nil {
		t.Fatalf("Quotas{}.Limit(ResourceKeys) returned unexpected error: %v", err)
	}
	if got != 0 {
		t.Errorf("Quotas{}.Limit(ResourceKeys) = %d, want 0", got)
	}
}

func TestQuotasLimitUnlimitedMinusOne(t *testing.T) {
	q := &Quotas{MaxDatabases: -1}
	got, err := q.Limit(ResourceDatabases)
	if err != nil {
		t.Fatalf("Quotas.Limit(ResourceDatabases) returned unexpected error: %v", err)
	}
	if got != -1 {
		t.Errorf("Quotas.Limit(ResourceDatabases) = %d, want -1", got)
	}
}

// --- Quotas.CurrentUsage() ---

func TestQuotasCurrentUsageReturnsCorrectFieldForEachResource(t *testing.T) {
	q := &Quotas{
		CurrentDatabases:    1,
		CurrentRaftGroups:   2,
		CurrentCollections:  3,
		CurrentKeys:         4,
		CurrentStorageBytes: 5,
		CurrentUsers:        6,
		CurrentRoles:        7,
		CurrentCertificates: 8,
		CurrentBackups:      9,
		CurrentVectorDims:   10,
		CurrentTSMetrics:    11,
	}
	tests := []struct {
		resource Resource
		want     int64
	}{
		{ResourceDatabases, 1},
		{ResourceRaftGroups, 2},
		{ResourceCollections, 3},
		{ResourceKeys, 4},
		{ResourceStorageBytes, 5},
		{ResourceUsers, 6},
		{ResourceRoles, 7},
		{ResourceCertificates, 8},
		{ResourceBackups, 9},
		{ResourceVectorDims, 10},
		{ResourceTSMetrics, 11},
	}
	for _, tc := range tests {
		got, err := q.CurrentUsage(tc.resource)
		if err != nil {
			t.Errorf("Quotas.CurrentUsage(%v) returned unexpected error: %v", tc.resource, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Quotas.CurrentUsage(%v) = %d, want %d", tc.resource, got, tc.want)
		}
	}
}

func TestQuotasCurrentUsageUnknownResourceReturnsError(t *testing.T) {
	q := &Quotas{}
	_, err := q.CurrentUsage(Resource(999))
	if err == nil {
		t.Fatal("Quotas.CurrentUsage(unknown) expected error, got nil")
	}
	if _, ok := err.(*InvalidResourceError); !ok {
		t.Errorf("error type = %T, want *InvalidResourceError", err)
	}
}

func TestQuotasCurrentUsageZeroValueReturnsZero(t *testing.T) {
	q := &Quotas{}
	got, err := q.CurrentUsage(ResourceKeys)
	if err != nil {
		t.Fatalf("Quotas{}.CurrentUsage(ResourceKeys) unexpected error: %v", err)
	}
	if got != 0 {
		t.Errorf("Quotas{}.CurrentUsage(ResourceKeys) = %d, want 0", got)
	}
}

// --- Quotas.SetCurrentUsage() ---

func TestQuotasSetCurrentUsageRoundTrip(t *testing.T) {
	resources := []Resource{
		ResourceDatabases, ResourceRaftGroups, ResourceCollections, ResourceKeys,
		ResourceStorageBytes, ResourceUsers, ResourceRoles, ResourceCertificates,
		ResourceBackups, ResourceVectorDims, ResourceTSMetrics,
	}
	for _, r := range resources {
		q := &Quotas{}
		if err := q.SetCurrentUsage(r, 42); err != nil {
			t.Errorf("SetCurrentUsage(%v, 42) unexpected error: %v", r, err)
			continue
		}
		got, err := q.CurrentUsage(r)
		if err != nil {
			t.Errorf("CurrentUsage(%v) after set unexpected error: %v", r, err)
			continue
		}
		if got != 42 {
			t.Errorf("CurrentUsage(%v) = %d after SetCurrentUsage(42), want 42", r, got)
		}
	}
}

func TestQuotasSetCurrentUsageClampNegativeToZero(t *testing.T) {
	q := &Quotas{}
	if err := q.SetCurrentUsage(ResourceKeys, -5); err != nil {
		t.Fatalf("SetCurrentUsage(ResourceKeys, -5) unexpected error: %v", err)
	}
	got, _ := q.CurrentUsage(ResourceKeys)
	if got != 0 {
		t.Errorf("SetCurrentUsage with negative value = %d, want 0 (clamped)", got)
	}
}

func TestQuotasSetCurrentUsageUnknownResourceReturnsError(t *testing.T) {
	q := &Quotas{}
	err := q.SetCurrentUsage(Resource(999), 10)
	if err == nil {
		t.Fatal("SetCurrentUsage(unknown) expected error, got nil")
	}
	if _, ok := err.(*InvalidResourceError); !ok {
		t.Errorf("error type = %T, want *InvalidResourceError", err)
	}
}
