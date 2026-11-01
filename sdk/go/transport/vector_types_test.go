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

package transport

import (
	"errors"
	"strings"
	"testing"
)

// TestParseVectorIndexType_ServerNames proves every name a server reports,
// in either case, resolves to the SDK constant the protobuf transports return.
func TestParseVectorIndexType_ServerNames(t *testing.T) {
	for name, want := range map[string]VectorIndexType{
		"HNSW":     IndexHNSW,
		"FLAT":     IndexFlat,
		"IVF":      IndexIVFFlat,
		"IVF_FLAT": IndexIVFFlat,
		"hnsw":     IndexHNSW,
		"flat":     IndexFlat,
		"ivf_flat": IndexIVFFlat,
	} {
		if got := ParseVectorIndexType(name); got != want {
			t.Errorf("ParseVectorIndexType(%q) = %q, want %q", name, got, want)
		}
	}
}

// TestParseVectorIndexType_UnknownPassesThrough proves a name the SDK does not
// know is reported as the server sent it, not replaced by a guess.
func TestParseVectorIndexType_UnknownPassesThrough(t *testing.T) {
	for _, name := range []string{"", "DISKANN", "lsh"} {
		if got := ParseVectorIndexType(name); got != VectorIndexType(name) {
			t.Errorf("ParseVectorIndexType(%q) = %q, want it unchanged", name, got)
		}
	}
}

// TestParseDistanceMetric_ServerNames proves every name a server reports, in
// either case, resolves to the SDK constant the protobuf transports return.
func TestParseDistanceMetric_ServerNames(t *testing.T) {
	for name, want := range map[string]DistanceMetric{
		"COSINE":      DistanceCosine,
		"EUCLIDEAN":   DistanceEuclidean,
		"L2":          DistanceEuclidean,
		"DOT_PRODUCT": DistanceDotProduct,
		"MANHATTAN":   DistanceManhattan,
		"cosine":      DistanceCosine,
		"l2":          DistanceEuclidean,
		"manhattan":   DistanceManhattan,
	} {
		got, err := ParseDistanceMetric(name)
		if err != nil {
			t.Fatalf("ParseDistanceMetric(%q): %v", name, err)
		}
		if got != want {
			t.Errorf("ParseDistanceMetric(%q) = %q, want %q", name, got, want)
		}
	}
}

// TestParseDistanceMetric_UnknownRefused proves a name the SDK does not know
// is refused with the typed error and never read as cosine.
func TestParseDistanceMetric_UnknownRefused(t *testing.T) {
	for _, name := range []string{"", "CHEBYSHEV", "hamming"} {
		got, err := ParseDistanceMetric(name)
		var unknown *UnknownDistanceMetricError
		if !errors.As(err, &unknown) {
			t.Fatalf("ParseDistanceMetric(%q) = %q, %v; want *UnknownDistanceMetricError", name, got, err)
		}
		if unknown.Metric != name || got != "" {
			t.Errorf("ParseDistanceMetric(%q) = %q, Metric %q; want empty and %q", name, got, unknown.Metric, name)
		}
		if !strings.Contains(err.Error(), "manhattan") {
			t.Errorf("error %q does not list the supported metrics", err)
		}
	}
}
