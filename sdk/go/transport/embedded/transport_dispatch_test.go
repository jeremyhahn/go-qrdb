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
	"errors"
	"strings"
	"testing"
)

// TestEmbeddedDispatch_HandlersBadRequest checks that every dispatch handler
// with a typed request refuses a request of the wrong type with the typed
// error naming its method.
func TestEmbeddedDispatch_HandlersBadRequest(t *testing.T) {
	tr := newExtTransport(t,
		WithLeaseService(&mockLeaseService{}),
		WithTimeSeriesService(&mockTSService{}),
		WithWatchService(&mockWatchService{}),
		WithCeremonyService(&mockCeremonyService{}),
		WithCeremonyCA(&mockCeremonyCA{}),
	)

	ctx := context.Background()
	bad := "not-the-right-type"
	cases := []string{
		"PutWithLease", "GetRevision", "GetAtRevision", "Range", "Compact",
		"ExecuteTransaction", "ExecuteBatch", "Watch", "WatchPrefix",
		"LeaseGrant", "LeaseRevoke", "LeaseKeepAlive", "LeaseTimeToLive",
		"TSWrite", "TSWriteBatch", "TSQuery", "TSAggregate", "TSWritePoint",
		"TSRichQuery", "TSDeleteSeries", "TSGetRetention", "TSSetRetention",
		"TSListMetrics",
		"CreateNamespace", "DeleteNamespace", "ListNamespaces", "GetNamespaceInfo",
		"UpsertVector", "GetVector", "DeleteVector", "BatchUpsert", "BatchDeleteVectors",
		"ListVectors", "UpdateMetadata", "SearchSimilar", "SearchByMetadata",
		"HybridSearch", "SearchByID", "RebuildIndex", "GetRebuildProgress",
		"GetIndexStats", "CompactNamespace", "GetCompactionProgress",
		"RetireJoinIdentity",
	}
	for _, m := range cases {
		t.Run(m, func(t *testing.T) {
			err := tr.Request(ctx, m, bad, nil)
			var ie *InvalidRequestError
			if !errors.As(err, &ie) {
				t.Fatalf("expected InvalidRequestError, got %T: %v", err, err)
			}
			if ie.Method != m || !strings.Contains(err.Error(), m) {
				t.Errorf("error = %q (method %q), want it to name %q", err, ie.Method, m)
			}
		})
	}
}

// TestEmbeddedDispatch_UnknownMethod checks that Request refuses a method it
// has no handler for with the typed error naming that method.
func TestEmbeddedDispatch_UnknownMethod(t *testing.T) {
	tr := newExtTransport(t)
	const m = "TotallyMadeUpMethod"
	err := tr.Request(context.Background(), m, nil, nil)
	var ue *UnknownMethodError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnknownMethodError, got %T: %v", err, err)
	}
	if ue.Method != m || !strings.Contains(err.Error(), m) {
		t.Errorf("error = %q (method %q), want it to name %q", err, ue.Method, m)
	}
}
