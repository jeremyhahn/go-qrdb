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

package compactionpb

import (
	"testing"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
)

func TestNilStaysNil(t *testing.T) {
	if FromProto(nil) != nil {
		t.Fatal("FromProto(nil) is not nil")
	}
	if ToProto(nil) != nil {
		t.Fatal("ToProto(nil) is not nil")
	}
}

func TestRoundTrip(t *testing.T) {
	for _, p := range []*compaction.Policy{
		{Mode: compaction.Revision, RetainRevisions: 42},
		{Mode: compaction.Period, RetainPeriod: "720h"},
	} {
		got := FromProto(ToProto(p))
		if *got != *p {
			t.Fatalf("round trip of %+v = %+v", p, got)
		}
	}
	c := &pb.DatabaseCompactionConfig{Mode: "period", RetainPeriod: "1h"}
	if back := ToProto(FromProto(c)); back.GetMode() != "period" || back.GetRetainPeriod() != "1h" {
		t.Fatalf("round trip of %v = %v", c, back)
	}
}
