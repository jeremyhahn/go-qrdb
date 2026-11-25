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

package transport

import (
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// TestEmbeddedOnlyOperations_MatchSealStoreClient pins the named exclusion
// to the SealStoreClient method set: a new seal-store verb cannot reach a
// remote transport without being listed (ruling 1234).
func TestEmbeddedOnlyOperations_MatchSealStoreClient(t *testing.T) {
	iface := reflect.TypeOf((*SealStoreClient)(nil)).Elem()
	methods := make([]string, 0, iface.NumMethod())
	for i := 0; i < iface.NumMethod(); i++ {
		methods = append(methods, iface.Method(i).Name)
	}
	listed := append([]string(nil), EmbeddedOnlyOperations...)
	sort.Strings(methods)
	sort.Strings(listed)
	if !reflect.DeepEqual(methods, listed) {
		t.Fatalf("SealStoreClient methods %v != EmbeddedOnlyOperations %v", methods, listed)
	}
	for _, op := range listed {
		if !IsEmbeddedOnly(op) {
			t.Errorf("IsEmbeddedOnly(%q) = false", op)
		}
	}
	for _, op := range []string{"BarrierSeal", "BarrierUnseal", "BarrierClusterUnseal", ""} {
		if IsEmbeddedOnly(op) {
			t.Errorf("IsEmbeddedOnly(%q) = true; seal and unseal stay on every transport", op)
		}
	}
}

// TestRefuseEmbeddedOnly_TypedError checks the refusal keeps both the
// sentinel and the typed error with the operation and transport.
func TestRefuseEmbeddedOnly_TypedError(t *testing.T) {
	err := RefuseEmbeddedOnly("grpc", OpSealStoreGet)
	if !errors.Is(err, ErrEmbeddedOnly) {
		t.Fatalf("errors.Is(%v, ErrEmbeddedOnly) = false", err)
	}
	var eo *EmbeddedOnlyError
	if !errors.As(err, &eo) {
		t.Fatalf("errors.As(%v, *EmbeddedOnlyError) = false", err)
	}
	if eo.Operation != OpSealStoreGet || eo.Transport != "grpc" {
		t.Fatalf("EmbeddedOnlyError = %+v", eo)
	}
	msg := err.Error()
	if !strings.Contains(msg, "embedded-only") || !strings.Contains(msg, OpSealStoreGet) || !strings.Contains(msg, "grpc") {
		t.Fatalf("Error() = %q; want it to name embedded-only, the operation and the transport", msg)
	}
}

// TestRefusalError_EmbeddedOnly rebuilds the typed refusal from the wire
// reason and metadata.
func TestRefusalError_EmbeddedOnly(t *testing.T) {
	err := RefusalError(ReasonEmbeddedOnly, map[string]string{"operation": OpSealStoreList, "transport": "rest"})
	var eo *EmbeddedOnlyError
	if !errors.As(err, &eo) || !errors.Is(err, ErrEmbeddedOnly) {
		t.Fatalf("RefusalError(EMBEDDED_ONLY) = %v; want *EmbeddedOnlyError", err)
	}
	if eo.Operation != OpSealStoreList || eo.Transport != "rest" {
		t.Fatalf("EmbeddedOnlyError = %+v", eo)
	}
	md := eo.Metadata()
	if md["operation"] != OpSealStoreList || md["transport"] != "rest" {
		t.Fatalf("Metadata() = %v", md)
	}
}
