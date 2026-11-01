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

package errclass

import (
	"fmt"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestClassify_VectorNamespaceRefusals pins that a namespace create the
// request itself cannot satisfy is the caller's input (REST 400, gRPC
// INVALID_ARGUMENT), bare or wrapped.
func TestClassify_VectorNamespaceRefusals(t *testing.T) {
	metric := &service.UnknownDistanceMetricError{Metric: "HAMMING"}
	indexType := &service.UnknownIndexTypeError{IndexType: "LSH"}
	cases := []struct {
		name string
		err  error
		want Class
	}{
		{"unknown distance metric", metric, InvalidArgument},
		{"unknown distance metric, wrapped", fmt.Errorf("create: %w", metric), InvalidArgument},
		{"unknown index type", indexType, InvalidArgument},
		{"unknown index type, wrapped", fmt.Errorf("create: %w", indexType), InvalidArgument},
	}
	for _, tc := range cases {
		if got := Classify(tc.err); got != tc.want {
			t.Errorf("%s: Classify(%T) = %v, want %v", tc.name, tc.err, got, tc.want)
		}
	}
}

// TestClassify_VectorIndexUntrained pins that a search on an IVF namespace
// too small to train is a failed precondition: waiting does not clear it,
// only more vectors do, so it must not read as retryable.
func TestClassify_VectorIndexUntrained(t *testing.T) {
	untrained := &service.VectorIndexUntrainedError{Namespace: "docs"}
	for _, err := range []error{untrained, fmt.Errorf("search: %w", untrained)} {
		if got := Classify(err); got != FailedPrecondition {
			t.Errorf("Classify(%v) = %v, want %v", err, got, FailedPrecondition)
		}
	}
}

func TestClassify_UnsupportedNamespaceGeometry(t *testing.T) {
	unsupported := &service.UnsupportedNamespaceGeometryError{Namespace: "docs", Field: "distance metric", Value: "HAMMING"}
	for _, err := range []error{unsupported, fmt.Errorf("search: %w", unsupported)} {
		if got := Classify(err); got != FailedPrecondition {
			t.Errorf("Classify(%v) = %v, want %v", err, got, FailedPrecondition)
		}
	}
}

func TestClassify_VectorMetricNotActive(t *testing.T) {
	notActive := &service.VectorMetricNotActiveError{IndexType: "FLAT", Feature: featuregate.VectorManhattan, ShardID: 7}
	for _, err := range []error{notActive, fmt.Errorf("create: %w", notActive)} {
		if got := Classify(err); got != FailedPrecondition {
			t.Errorf("Classify(%v) = %v, want %v", err, got, FailedPrecondition)
		}
	}
}

func TestClassify_VectorMetricNotActiveIsNotRetryable(t *testing.T) {
	if got := Classify(&service.VectorMetricNotActiveError{IndexType: "IVF", Feature: featuregate.VectorManhattan}); got == Unavailable {
		t.Errorf("Classify = %v: a create waiting on an upgrade must not read as transient", got)
	}
}
