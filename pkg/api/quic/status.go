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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
)

// wireStatusByGRPCCode is the correspondence between gRPC's status codes and
// this wire's statuses, written down exactly once.
//
// It is a bijection: seventeen codes, seventeen statuses, every code mapped
// and no two codes sharing a status. TestWireStatusByGRPCCode_IsABijection
// asserts both halves, so a status added without its row — or a row that
// duplicates another's status — fails the build rather than degrading a live
// condition to StatusError.
//
// The numbering differs on the two sides because this wire's codes 0-5 predate
// the correspondence and are not renumbered (see the wire package's Status
// documentation). That is precisely why the mapping is a table and not a cast:
// StatusNotFound is 6 and codes.NotFound is 5, and any arithmetic relating
// them would be wrong for fifteen of the seventeen rows.
//
// codes.Internal maps to StatusError, which is this protocol's fallback name
// for the same condition and predates the vocabulary. codes.Unknown gets its
// own StatusUnknown rather than sharing StatusError, because the two are
// genuinely different claims: Unknown is the ORIGIN saying it could not
// classify, StatusError is this server declining to.
var wireStatusByGRPCCode = map[codes.Code]uint16{
	codes.OK:                 wire.StatusOK,
	codes.Internal:           wire.StatusError,
	codes.Unimplemented:      wire.StatusUnimplemented,
	codes.Unauthenticated:    wire.StatusUnauthenticated,
	codes.PermissionDenied:   wire.StatusPermissionDenied,
	codes.Unavailable:        wire.StatusUnavailable,
	codes.NotFound:           wire.StatusNotFound,
	codes.InvalidArgument:    wire.StatusInvalidArgument,
	codes.AlreadyExists:      wire.StatusAlreadyExists,
	codes.FailedPrecondition: wire.StatusFailedPrecondition,
	codes.ResourceExhausted:  wire.StatusResourceExhausted,
	codes.Aborted:            wire.StatusAborted,
	codes.OutOfRange:         wire.StatusOutOfRange,
	codes.Canceled:           wire.StatusCanceled,
	codes.DataLoss:           wire.StatusDataLoss,
	codes.DeadlineExceeded:   wire.StatusDeadlineExceeded,
	codes.Unknown:            wire.StatusUnknown,
}

// moduleStatus translates a gRPC status error raised by a PROJECTED
// capability module into a wire status.
//
// registerModules dispatches a module's unary grpc.ServiceDesc methods as QUIC
// frames (see modules.go), so a module's handler answers in gRPC's vocabulary
// by construction — it has never seen this wire and cannot be asked to. Before
// the vocabulary existed every one of those answers arrived here unclassified
// and left as StatusError, so a module's NotFound and its Internal were the
// same frame.
//
// The second return reports whether err carried a gRPC status at all. It is
// false for every ordinary Go error, including all of qrdb's own typed service
// errors, which is what keeps this clause from shadowing the classification
// mapServiceError does for them.
func moduleStatus(err error) (uint16, bool) {
	st, ok := status.FromError(err)
	if !ok {
		// status.FromError reports false for an error that carries no gRPC
		// status, and hands back a codes.Unknown status it synthesised. Using
		// that would map every unclassified error in the process to
		// StatusUnknown.
		return 0, false
	}
	ws, known := wireStatusByGRPCCode[st.Code()]
	return ws, known
}
