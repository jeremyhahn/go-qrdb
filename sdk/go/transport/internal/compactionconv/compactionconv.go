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

// Package compactionconv converts scheduled-compaction policies between the
// transport-neutral type and the protobuf message. The gRPC, Unix and QUIC
// transports exchange the same protobuf message, so the mapping lives here
// once. It is internal: only the transport implementations may import it.
package compactionconv

import (
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ToProto converts a policy to its protobuf message; nil stays nil (off).
func ToProto(c *transport.DatabaseCompactionConfig) *pb.DatabaseCompactionConfig {
	if c == nil {
		return nil
	}
	return &pb.DatabaseCompactionConfig{
		Mode:            c.Mode,
		RetainRevisions: c.RetainRevisions,
		RetainPeriod:    c.RetainPeriod,
	}
}

// FromProto converts a protobuf policy; nil stays nil (none).
func FromProto(c *pb.DatabaseCompactionConfig) *transport.DatabaseCompactionConfig {
	if c == nil {
		return nil
	}
	return &transport.DatabaseCompactionConfig{
		Mode:            c.GetMode(),
		RetainRevisions: c.GetRetainRevisions(),
		RetainPeriod:    c.GetRetainPeriod(),
	}
}
