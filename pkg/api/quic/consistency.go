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
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// readOptsFromWire maps a proto ConsistencyLevel to consistency read options.
// The second return value is false when the level is UNSPECIFIED, signalling
// the handler to use the default (linearizable) read path so clients omitting
// the field are unaffected.
func readOptsFromWire(level pb.ConsistencyLevel) (consistency.ReadOptions, bool) {
	switch level {
	case pb.ConsistencyLevel_CONSISTENCY_LEVEL_STRONG:
		return consistency.ReadOptions{Consistency: consistency.Strong}, true
	case pb.ConsistencyLevel_CONSISTENCY_LEVEL_LINEARIZABLE:
		return consistency.ReadOptions{Consistency: consistency.Linearizable}, true
	case pb.ConsistencyLevel_CONSISTENCY_LEVEL_EVENTUAL:
		return consistency.ReadOptions{Consistency: consistency.Eventual, AllowStale: true}, true
	default:
		return consistency.ReadOptions{}, false
	}
}

// vectorConsistencyFromWire maps a proto ConsistencyLevel to the vector
// service's ConsistencyLevel. UNSPECIFIED maps to ConsistencyStrong, which
// is the zero value and preserves the behavior of clients that formerly sent
// an int32 field value of 0.
//
// Proto enum → service mapping:
//
//	UNSPECIFIED (0) → ConsistencyStrong (0)   // backward-compat default
//	STRONG      (1) → ConsistencyStrong (0)
//	LINEARIZABLE(2) → ConsistencyLinearizable (1)
//	EVENTUAL    (3) → ConsistencyEventual (2)
func vectorConsistencyFromWire(level pb.ConsistencyLevel) service.ConsistencyLevel {
	switch level {
	case pb.ConsistencyLevel_CONSISTENCY_LEVEL_LINEARIZABLE:
		return service.ConsistencyLinearizable
	case pb.ConsistencyLevel_CONSISTENCY_LEVEL_EVENTUAL:
		return service.ConsistencyEventual
	default:
		// UNSPECIFIED and STRONG both map to ConsistencyStrong.
		return service.ConsistencyStrong
	}
}

// writeModeFromWire maps a proto WriteMode to a writemode.Mode. The second
// return value is false when the mode is UNSPECIFIED, signalling the handler
// to use the default (sync/quorum) write path.
func writeModeFromWire(mode pb.WriteMode) (writemode.Mode, bool) {
	switch mode {
	case pb.WriteMode_WRITE_MODE_SYNC:
		return writemode.Sync, true
	case pb.WriteMode_WRITE_MODE_WAL_DURABLE:
		return writemode.WALDurable, true
	case pb.WriteMode_WRITE_MODE_BATCHED:
		return writemode.Batched, true
	default:
		return writemode.Sync, false
	}
}
