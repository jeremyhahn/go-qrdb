// Copyright 2026 Jeremy Hahn
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

package main

import (
	"context"
	"encoding/pem"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
)

// TestCeremonyCAFromBundle_UndecodableDER pins that a CA certificate or CA
// key that is a well-formed PEM block around undecodable DER is refused with
// the typed parse error naming which half failed. The corrupt-material tests
// beside ceremonyCAFromBundle's own cover input that is not PEM at all.
func TestCeremonyCAFromBundle_UndecodableDER(t *testing.T) {
	junk := func(blockType string) []byte {
		return pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: []byte("not der")})
	}
	tests := []struct {
		name    string
		mutate  func(*TLSBundle)
		wantKey bool
	}{
		{name: "cert", mutate: func(b *TLSBundle) { b.CACertPEM = junk("CERTIFICATE") }},
		{name: "key", mutate: func(b *TLSBundle) { b.CAKeyPEM = junk("EC PRIVATE KEY") }, wantKey: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bundle, err := generateCATLS()
			if err != nil {
				t.Fatalf("generateCATLS: %v", err)
			}
			tt.mutate(bundle)

			ca, err := ceremonyCAFromBundle(bundle)
			if ca != nil {
				t.Fatalf("ceremonyCAFromBundle returned a CA for undecodable material")
			}
			var caErr *CeremonyCAError
			if !errors.As(err, &caErr) {
				t.Fatalf("err = %v, want *CeremonyCAError", err)
			}
			var certErr *ceremony.CACertParseError
			var keyErr *ceremony.CAKeyParseError
			if tt.wantKey && !errors.As(err, &keyErr) {
				t.Fatalf("err = %v, want *ceremony.CAKeyParseError", err)
			}
			if !tt.wantKey && !errors.As(err, &certErr) {
				t.Fatalf("err = %v, want *ceremony.CACertParseError", err)
			}
		})
	}
}

// TestAwaitUnseal_UnknownAuditPolicy pins that the sealed window refuses an
// audit failure policy this build does not know before it builds any seal
// service, rather than resolving it to either posture.
func TestAwaitUnseal_UnknownAuditPolicy(t *testing.T) {
	node, err := awaitUnseal(context.Background(), &startFlags{auditFailurePolicy: "sometimes"}, nil, nil, nil, nil, nil)
	if node != nil {
		t.Fatalf("awaitUnseal returned a node for an unknown audit policy")
	}
	var policyErr *audit.InvalidFailurePolicyError
	if !errors.As(err, &policyErr) || policyErr.Value != "sometimes" {
		t.Fatalf("err = %v, want *audit.InvalidFailurePolicyError for %q", err, "sometimes")
	}
}
