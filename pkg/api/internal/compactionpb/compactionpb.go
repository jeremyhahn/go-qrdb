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

// Package compactionpb converts scheduled-compaction policies between the
// domain type and the protobuf message the gRPC, Unix and QUIC transports
// carry, so those transports cannot drift apart on the mapping.
package compactionpb

import (
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
)

// FromProto converts a DatabaseCompactionConfig to a policy; nil stays nil
// (off).
func FromProto(c *pb.DatabaseCompactionConfig) *compaction.Policy {
	if c == nil {
		return nil
	}
	return &compaction.Policy{
		Mode:            compaction.Mode(c.GetMode()),
		RetainRevisions: c.GetRetainRevisions(),
		RetainPeriod:    c.GetRetainPeriod(),
	}
}

// ToProto converts a policy to its DatabaseCompactionConfig; nil stays nil
// (none).
func ToProto(p *compaction.Policy) *pb.DatabaseCompactionConfig {
	if p == nil {
		return nil
	}
	return &pb.DatabaseCompactionConfig{
		Mode:            string(p.Mode),
		RetainRevisions: p.RetainRevisions,
		RetainPeriod:    p.RetainPeriod,
	}
}
