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

package lifecycle_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// manhattanFlat is a create whose namespace record carries the Manhattan
// metric value, which waits for the vector-manhattan cluster feature.
func manhattanFlat() *service.CreateNamespaceRequest {
	return &service.CreateNamespaceRequest{
		Namespace: "gated", Dimension: 2, DistanceMetric: "manhattan", IndexType: "flat",
	}
}

// The vector store is node-local, so the built vector service reads the
// cluster's feature gates from the host: on a host whose groups can hold
// other binaries, a Manhattan FLAT namespace is refused until
// vector-manhattan is activated.
func TestBuildServices_VectorManhattanFlatIsGatedOnTheHost(t *testing.T) {
	host := unactivatedHost{LocalBackend: newLocalHost()}
	t.Cleanup(func() { _ = host.Close() })
	node, _ := buildRoutedIAMNodeOn(t, host)

	_, err := node.Vector.CreateNamespace(context.Background(), manhattanFlat())
	var notActive *service.VectorMetricNotActiveError
	if !errors.As(err, &notActive) || notActive.Feature != featuregate.VectorManhattan {
		t.Fatalf("CreateNamespace(manhattan, flat) before activation = %v, want VectorMetricNotActiveError(%s)", err, featuregate.VectorManhattan)
	}
}

// A standalone node holds only this binary and gates nothing: the same
// namespace is created.
func TestBuildServices_StandaloneVectorManhattanFlatIsNotGated(t *testing.T) {
	node, _ := buildRoutedIAMNode(t)

	if _, err := node.Vector.CreateNamespace(context.Background(), manhattanFlat()); err != nil {
		t.Fatalf("CreateNamespace(manhattan, flat) on a standalone node: %v", err)
	}
}
