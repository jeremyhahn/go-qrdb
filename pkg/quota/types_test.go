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
