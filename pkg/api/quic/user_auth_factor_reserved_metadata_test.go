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

// QUIC half of the go-qrdb#234 cross-transport verification.
//
// The issue named only the REST and gRPC handlers. The native QUIC transport
// carries the same caller-supplied Metadata into the same service and was not
// listed, which is why this file exists: fixing at the service is what makes
// an unlisted transport inherit the rule, and this asserts that it did.
package quic

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

func realUserFactorService(t *testing.T) service.UserAuthFactorServicer {
	t.Helper()
	store := map[string][]byte{}
	kv := &mockKVService{
		putFn: func(_ context.Context, key string, value []byte) error {
			store[key] = append([]byte(nil), value...)
			return nil
		},
		getFn: func(_ context.Context, key string) ([]byte, error) {
			return store[key], nil
		},
		listFn: func(_ context.Context, prefix string) ([]string, error) {
			var out []string
			for k := range store {
				if strings.HasPrefix(k, prefix) {
					out = append(out, k)
				}
			}
			return out, nil
		},
	}
	svc, err := service.NewUserAuthFactorService(service.UserAuthFactorServiceConfig{KV: kv})
	if err != nil {
		t.Fatalf("NewUserAuthFactorService: %v", err)
	}
	return svc
}

func assertReservedRejectedFrame(t *testing.T, err error, wantKey string) {
	t.Helper()
	if err == nil {
		t.Fatal("frame handler succeeded with a server-owned metadata key present")
	}
	var reserved *auth.ReservedMetadataKeyError
	if !errors.As(err, &reserved) {
		t.Fatalf("error = %v (%T), want *auth.ReservedMetadataKeyError", err, err)
	}
	if reserved.Key != wantKey {
		t.Errorf("error names key %q, want %q", reserved.Key, wantKey)
	}
	if reserved.Prefix != auth.ReservedMetadataPrefix {
		t.Errorf("error names prefix %q, want %q", reserved.Prefix, auth.ReservedMetadataPrefix)
	}
}

func TestQUIC_AddRefusesReservedMetadata(t *testing.T) {
	s := &Server{userAuthFactorSvc: realUserFactorService(t)}

	payload, _ := json.Marshal(quicUserFactorAddRequest{
		UserID:     42,
		Type:       "mtls",
		Credential: []byte("cred"),
		Metadata:   map[string]string{"attest_level": "hardware"},
	})
	_, err := s.handleUserAuthFactorAdd(context.Background(), payload)
	assertReservedRejectedFrame(t, err, "attest_level")
}

func TestQUIC_RotateRefusesReservedMetadata(t *testing.T) {
	svc := realUserFactorService(t)
	s := &Server{userAuthFactorSvc: svc}

	seed := &auth.AuthFactor{
		ID: "old", UserID: 42, Type: auth.AuthFactorMTLS, Credential: []byte("cred-old"),
	}
	if err := svc.Add(context.Background(), 42, seed); err != nil {
		t.Fatalf("seed Add: %v", err)
	}

	payload, _ := json.Marshal(quicUserFactorRotateRequest{
		OldFactorID: "old",
		NewFactor: quicUserFactorAddRequest{
			UserID:     42,
			Type:       "mtls",
			Credential: []byte("cred-new"),
			Metadata:   map[string]string{"attest_aaguid": "forged"},
		},
	})
	_, err := s.handleUserAuthFactorRotate(context.Background(), payload)
	assertReservedRejectedFrame(t, err, "attest_aaguid")
}

// TestQUIC_AddAllowsOrdinaryMetadata is the control: a namespace, not a ban.
func TestQUIC_AddAllowsOrdinaryMetadata(t *testing.T) {
	s := &Server{userAuthFactorSvc: realUserFactorService(t)}

	payload, _ := json.Marshal(quicUserFactorAddRequest{
		UserID:     42,
		Type:       "mtls",
		Credential: []byte("cred"),
		Metadata:   map[string]string{"vendor": "yubico"},
	})
	out, err := s.handleUserAuthFactorAdd(context.Background(), payload)
	if err != nil {
		t.Fatalf("Add with ordinary metadata: %v", err)
	}
	if !strings.Contains(string(out), "yubico") {
		t.Errorf("ordinary metadata not round-tripped: %s", out)
	}
}
