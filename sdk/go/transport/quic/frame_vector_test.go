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

package quic

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// vectorCalls is every vector call on tr, each with the smallest request it
// accepts.
func vectorCalls(ctx context.Context, tr *Transport) map[string]func() error {
	return map[string]func() error{
		"CreateNamespace": func() error {
			_, err := tr.CreateNamespace(ctx, &transport.CreateNamespaceRequest{Namespace: "ns", Dimension: 2})
			return err
		},
		"DeleteNamespace": func() error {
			_, err := tr.DeleteNamespace(ctx, &transport.DeleteNamespaceRequest{Namespace: "ns"})
			return err
		},
		"ListNamespaces": func() error {
			_, err := tr.ListNamespaces(ctx, &transport.ListNamespacesRequest{})
			return err
		},
		"GetNamespaceInfo": func() error {
			_, err := tr.GetNamespaceInfo(ctx, &transport.GetNamespaceInfoRequest{Namespace: "ns"})
			return err
		},
		"UpsertVector": func() error {
			_, err := tr.UpsertVector(ctx, &transport.UpsertVectorRequest{Namespace: "ns"})
			return err
		},
		"GetVector": func() error {
			_, err := tr.GetVector(ctx, &transport.GetVectorRequest{Namespace: "ns"})
			return err
		},
		"DeleteVector": func() error {
			_, err := tr.DeleteVector(ctx, &transport.DeleteVectorRequest{Namespace: "ns"})
			return err
		},
		"BatchUpsert": func() error {
			_, err := tr.BatchUpsert(ctx, &transport.BatchUpsertRequest{Namespace: "ns"})
			return err
		},
		"BatchDeleteVectors": func() error {
			_, err := tr.BatchDeleteVectors(ctx, &transport.BatchDeleteVectorsRequest{Namespace: "ns", IDs: []uint64{1}})
			return err
		},
		"ListVectors": func() error {
			_, err := tr.ListVectors(ctx, &transport.ListVectorsRequest{Namespace: "ns"})
			return err
		},
		"UpdateMetadata": func() error {
			_, err := tr.UpdateMetadata(ctx, &transport.UpdateVectorMetadataRequest{Namespace: "ns"})
			return err
		},
		"SearchSimilar": func() error {
			_, err := tr.SearchSimilar(ctx, &transport.SearchSimilarRequest{Namespace: "ns"})
			return err
		},
		"SearchByMetadata": func() error {
			_, err := tr.SearchByMetadata(ctx, &transport.SearchByMetadataRequest{Namespace: "ns"})
			return err
		},
		"HybridSearch": func() error {
			_, err := tr.HybridSearch(ctx, &transport.HybridSearchRequest{Namespace: "ns"})
			return err
		},
		"SearchByID": func() error {
			_, err := tr.SearchByID(ctx, &transport.SearchByIDRequest{Namespace: "ns"})
			return err
		},
		"RebuildIndex": func() error {
			_, err := tr.RebuildIndex(ctx, &transport.RebuildIndexRequest{Namespace: "ns"})
			return err
		},
		"GetRebuildProgress": func() error {
			_, err := tr.GetRebuildProgress(ctx, &transport.GetRebuildProgressRequest{Namespace: "ns"})
			return err
		},
		"GetIndexStats": func() error {
			_, err := tr.GetIndexStats(ctx, &transport.GetIndexStatsRequest{Namespace: "ns"})
			return err
		},
		"CompactNamespace": func() error {
			_, err := tr.CompactNamespace(ctx, &transport.CompactNamespaceRequest{Namespace: "ns"})
			return err
		},
		"GetCompactionProgress": func() error {
			_, err := tr.GetCompactionProgress(ctx, &transport.GetCompactionProgressRequest{Namespace: "ns"})
			return err
		},
	}
}

// TestFrameTransport_VectorCallsRefuseAnUndecodableAnswer proves every vector
// call reports an answer it cannot decode as an unmarshal error rather than
// returning a zero result.
func TestFrameTransport_VectorCallsRefuseAnUndecodableAnswer(t *testing.T) {
	tr := NewFrameTransport("", func(context.Context, string, string, []byte) (uint16, []byte, error) {
		return wire.StatusOK, []byte{0xff, 0xff, 0xff}, nil
	})
	calls := vectorCalls(context.Background(), tr)
	// These three read no answer body: success is the OK status alone.
	for _, name := range []string{"DeleteNamespace", "DeleteVector", "UpdateMetadata"} {
		delete(calls, name)
	}
	for name, call := range calls {
		if err := call(); !errors.As(err, new(*unmarshalError)) {
			t.Errorf("%s = %v; want an unmarshal error", name, err)
		}
	}
}

// TestFrameTransport_VectorCallsSurfaceTheSendersError proves every vector
// call returns the sender's failure.
func TestFrameTransport_VectorCallsSurfaceTheSendersError(t *testing.T) {
	tr := NewFrameTransport("", func(context.Context, string, string, []byte) (uint16, []byte, error) {
		return 0, nil, failure{}
	})
	for name, call := range vectorCalls(context.Background(), tr) {
		if err := call(); !errors.As(err, new(failure)) {
			t.Errorf("%s = %v; want the sender's failure", name, err)
		}
	}
}
