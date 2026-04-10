// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cert

import (
	"encoding/json"
	"testing"
	"time"
)

func TestCertRecord_JSONRoundTrip(t *testing.T) {
	now := time.Date(2026, 3, 15, 10, 30, 0, 0, time.UTC)
	rec := &CertRecord{
		Serial:    "abc123def456",
		Username:  "alice",
		TenantID:  "tenant-1",
		IssuedAt:  now,
		ExpiresAt: now.Add(8760 * time.Hour),
		Revoked:   false,
	}

	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var decoded CertRecord
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if decoded.Serial != rec.Serial {
		t.Errorf("Serial: got %q, want %q", decoded.Serial, rec.Serial)
	}
	if decoded.Username != rec.Username {
		t.Errorf("Username: got %q, want %q", decoded.Username, rec.Username)
	}
	if decoded.TenantID != rec.TenantID {
		t.Errorf("TenantID: got %q, want %q", decoded.TenantID, rec.TenantID)
	}
	if !decoded.IssuedAt.Equal(rec.IssuedAt) {
		t.Errorf("IssuedAt: got %v, want %v", decoded.IssuedAt, rec.IssuedAt)
	}
	if !decoded.ExpiresAt.Equal(rec.ExpiresAt) {
		t.Errorf("ExpiresAt: got %v, want %v", decoded.ExpiresAt, rec.ExpiresAt)
	}
	if decoded.Revoked != rec.Revoked {
		t.Errorf("Revoked: got %v, want %v", decoded.Revoked, rec.Revoked)
	}
}

func TestCertRecord_JSONRoundTrip_Revoked(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	rec := &CertRecord{
		Serial:    "revoked-serial",
		Username:  "bob",
		TenantID:  "org-42",
		IssuedAt:  now,
		ExpiresAt: now.Add(24 * time.Hour),
		Revoked:   true,
	}

	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var decoded CertRecord
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if !decoded.Revoked {
		t.Error("expected Revoked to be true after round-trip")
	}
	if decoded.TenantID != "org-42" {
		t.Errorf("TenantID: got %q, want %q", decoded.TenantID, "org-42")
	}
}

func TestCertRecord_JSONFields(t *testing.T) {
	rec := &CertRecord{
		Serial:    "s1",
		Username:  "u1",
		TenantID:  "t1",
		IssuedAt:  time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(time.Hour),
		Revoked:   true,
	}

	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal to map: %v", err)
	}

	expectedFields := []string{"serial", "username", "tenantID", "issuedAt", "expiresAt", "revoked"}
	for _, field := range expectedFields {
		if _, ok := raw[field]; !ok {
			t.Errorf("expected JSON field %q not found", field)
		}
	}
}

func TestGenerateRequest_ZeroTTL(t *testing.T) {
	req := &GenerateRequest{
		Username: "alice",
		TenantID: "default",
	}
	if req.TTL != 0 {
		t.Errorf("default TTL: got %v, want 0", req.TTL)
	}
}

func TestCertBundle_Fields(t *testing.T) {
	bundle := &CertBundle{
		CertPEM: []byte("cert-pem"),
		KeyPEM:  []byte("key-pem"),
		Serial:  "serial-1",
	}
	if string(bundle.CertPEM) != "cert-pem" {
		t.Errorf("CertPEM: got %q, want %q", bundle.CertPEM, "cert-pem")
	}
	if string(bundle.KeyPEM) != "key-pem" {
		t.Errorf("KeyPEM: got %q, want %q", bundle.KeyPEM, "key-pem")
	}
	if bundle.Serial != "serial-1" {
		t.Errorf("Serial: got %q, want %q", bundle.Serial, "serial-1")
	}
}
