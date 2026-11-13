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

package embedded

import (
	"context"
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/publicverbs"
)

// TestPublicVerbsParity_Embedded fails when a publicverbs.Table row names an
// embedded method the Transport does not have, so the table cannot drift
// from the embedded surface it describes.
func TestPublicVerbsParity_Embedded(t *testing.T) {
	typ := reflect.TypeFor[*Transport]()
	for _, v := range publicverbs.Verbs {
		name := publicverbs.Table[v].Embedded
		if name == "" {
			t.Errorf("%s has no Embedded name in publicverbs.Table", v)
			continue
		}
		if _, ok := typ.MethodByName(name); !ok {
			t.Errorf("publicverbs.Table names embedded %s (%s), but *Transport has no such method", name, v)
		}
	}
}

// TestPublicVerbsParity_EmbeddedHealthNeedsNoIdentity pins that the
// embedded Health, the table's health row, is served to a context carrying
// no identity at all, as every network transport serves it to a caller with
// no certificate.
func TestPublicVerbsParity_EmbeddedHealthNeedsNoIdentity(t *testing.T) {
	if got := publicverbs.Table[publicverbs.VerbHealth].Embedded; got != "Health" {
		t.Fatalf("publicverbs.Table health Embedded = %q, want Health", got)
	}
	tr, err := New(newMockKVService(), &mockAdminService{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })

	resp, err := tr.Health(context.Background())
	if err != nil {
		t.Fatalf("Health with no identity: %v", err)
	}
	if resp == nil {
		t.Fatal("Health with no identity returned a nil response")
	}
}
