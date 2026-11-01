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
	"errors"
	"testing"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestDistanceMetricToProto_KnownNames proves every SDK metric name, in any
// case, maps to its own wire value, Manhattan included, and an unset name to
// the wire default.
func TestDistanceMetricToProto_KnownNames(t *testing.T) {
	for name, want := range map[string]pb.DistanceMetric{
		"":                           pb.DistanceMetric_DISTANCE_COSINE,
		transport.DistanceCosine:     pb.DistanceMetric_DISTANCE_COSINE,
		transport.DistanceEuclidean:  pb.DistanceMetric_DISTANCE_EUCLIDEAN,
		"L2":                         pb.DistanceMetric_DISTANCE_EUCLIDEAN,
		transport.DistanceDotProduct: pb.DistanceMetric_DISTANCE_DOT_PRODUCT,
		transport.DistanceManhattan:  pb.DistanceMetric_DISTANCE_MANHATTAN,
		"MANHATTAN":                  pb.DistanceMetric_DISTANCE_MANHATTAN,
	} {
		got, err := distanceMetricToProto(name)
		if err != nil {
			t.Fatalf("distanceMetricToProto(%q): %v", name, err)
		}
		if got != want {
			t.Errorf("distanceMetricToProto(%q) = %v, want %v", name, got, want)
		}
	}
}

// TestDistanceMetricToProto_UnknownRefused proves a name with no wire value
// is refused with the typed error rather than sent as cosine.
func TestDistanceMetricToProto_UnknownRefused(t *testing.T) {
	_, err := distanceMetricToProto("chebyshev")
	var unknown *transport.UnknownDistanceMetricError
	if !errors.As(err, &unknown) || unknown.Metric != "chebyshev" {
		t.Fatalf("distanceMetricToProto(chebyshev) error = %v, want *UnknownDistanceMetricError{chebyshev}", err)
	}
}

// TestDistanceMetricFromProto_RoundTrip proves every wire value maps back to
// the SDK name it came from.
func TestDistanceMetricFromProto_RoundTrip(t *testing.T) {
	for _, name := range []string{transport.DistanceCosine, transport.DistanceEuclidean, transport.DistanceDotProduct, transport.DistanceManhattan} {
		wire, err := distanceMetricToProto(name)
		if err != nil {
			t.Fatalf("distanceMetricToProto(%q): %v", name, err)
		}
		got, err := distanceMetricFromProto(wire)
		if err != nil {
			t.Fatalf("distanceMetricFromProto(%v): %v", wire, err)
		}
		if got != name {
			t.Errorf("round trip of %q = %q", name, got)
		}
	}
}

// TestDistanceMetricFromProto_UnknownRefused proves a wire value this SDK
// does not know is refused with the typed error, never read as cosine.
func TestDistanceMetricFromProto_UnknownRefused(t *testing.T) {
	got, err := distanceMetricFromProto(pb.DistanceMetric(99))
	var unknown *transport.UnknownDistanceMetricError
	if !errors.As(err, &unknown) {
		t.Fatalf("distanceMetricFromProto(99) = %q, %v; want *UnknownDistanceMetricError", got, err)
	}
	if got != "" || unknown.Metric != "99" {
		t.Errorf("distanceMetricFromProto(99) = %q, Metric %q; want empty and \"99\"", got, unknown.Metric)
	}
}
