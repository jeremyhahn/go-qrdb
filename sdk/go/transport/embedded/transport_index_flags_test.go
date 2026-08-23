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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// captureEmbeddedIndexConfig drives the embedded transport's IndexCreate and
// returns the index.IndexConfig it handed to the injected IndexServicer.
func captureEmbeddedIndexConfig(t *testing.T, cfg transport.IndexConfig) index.IndexConfig {
	t.Helper()
	var got index.IndexConfig
	svc := &mockIndexService{
		createFn: func(_ context.Context, _, _ string, c index.IndexConfig) error {
			got = c
			return nil
		},
	}
	tr := newExtTransport(t, WithIndexService(svc))
	if err := tr.IndexCreate(context.Background(), "idx", "field", cfg); err != nil {
		t.Fatalf("IndexCreate: %v", err)
	}
	return got
}

// TestEmbeddedIndex_Create_CaseInsensitive covers go-qrdb#34 on the embedded
// transport, the one path with no wire in between. The translation is still a
// place a flag can be dropped: this transport builds an index.IndexConfig by
// hand from transport.IndexConfig.
func TestEmbeddedIndex_Create_CaseInsensitive(t *testing.T) {
	got := captureEmbeddedIndexConfig(t, transport.IndexConfig{CaseInsensitive: true})
	if got.CaseSensitive {
		t.Error("a case-insensitive config produced CaseSensitive:true")
	}
}

// TestEmbeddedIndex_Create_ZeroConfigIsCaseSensitive is the control and the
// polarity guard.
//
// This is the sharpest instance of the trap: the transport builds its
// index.IndexConfig with a composite literal, and index.IndexConfig names
// CaseSensitive for the DEFAULT. Simply omitting the field -- which is what
// the code did before #34, when nothing read it -- yields a case-INSENSITIVE
// index for every embedded caller.
func TestEmbeddedIndex_Create_ZeroConfigIsCaseSensitive(t *testing.T) {
	got := captureEmbeddedIndexConfig(t, transport.IndexConfig{})
	if !got.CaseSensitive {
		t.Error("a zero config produced a case-INSENSITIVE index; the default must " +
			"stay case-sensitive")
	}
}
