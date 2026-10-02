// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package wire

import "google.golang.org/grpc/codes"

// statusByGRPCCode is the correspondence between gRPC's status codes and
// this wire's statuses, written down exactly once.
//
// It is a bijection: seventeen codes, seventeen statuses, every code mapped
// and no two codes sharing a status. TestStatusByGRPCCode_IsABijection
// asserts both halves, so a status added without its row — or a row that
// duplicates another's status — fails the build rather than degrading a live
// condition to StatusError.
//
// The numbering differs on the two sides because this wire's codes 0-5 predate
// the correspondence and are not renumbered (see the Status documentation). That is precisely why the mapping is a table and not a cast:
// StatusNotFound is 6 and codes.NotFound is 5, and any arithmetic relating
// them would be wrong for fifteen of the seventeen rows.
//
// codes.Internal maps to StatusError, which is this protocol's fallback name
// for the same condition and predates the vocabulary. codes.Unknown gets its
// own StatusUnknown rather than sharing StatusError, because the two are
// genuinely different claims: Unknown is the ORIGIN saying it could not
// classify, StatusError is this server declining to.
var statusByGRPCCode = map[codes.Code]uint16{
	codes.OK:                 StatusOK,
	codes.Internal:           StatusError,
	codes.Unimplemented:      StatusUnimplemented,
	codes.Unauthenticated:    StatusUnauthenticated,
	codes.PermissionDenied:   StatusPermissionDenied,
	codes.Unavailable:        StatusUnavailable,
	codes.NotFound:           StatusNotFound,
	codes.InvalidArgument:    StatusInvalidArgument,
	codes.AlreadyExists:      StatusAlreadyExists,
	codes.FailedPrecondition: StatusFailedPrecondition,
	codes.ResourceExhausted:  StatusResourceExhausted,
	codes.Aborted:            StatusAborted,
	codes.OutOfRange:         StatusOutOfRange,
	codes.Canceled:           StatusCanceled,
	codes.DataLoss:           StatusDataLoss,
	codes.DeadlineExceeded:   StatusDeadlineExceeded,
	codes.Unknown:            StatusUnknown,
}

// grpcCodeByStatus is statusByGRPCCode read the other way. It is derived, not
// written, so the two directions cannot disagree.
var grpcCodeByStatus = func() map[uint16]codes.Code {
	m := make(map[uint16]codes.Code, len(statusByGRPCCode))
	for code, st := range statusByGRPCCode {
		m[st] = code
	}
	return m
}()

// StatusForGRPCCode reports the wire status that carries code's condition,
// and whether code is one gRPC defines.
func StatusForGRPCCode(code codes.Code) (uint16, bool) {
	st, ok := statusByGRPCCode[code]
	return st, ok
}

// GRPCCodeForStatus reports the gRPC code for a wire status's condition, and
// whether this build declares the status.
func GRPCCodeForStatus(status uint16) (codes.Code, bool) {
	code, ok := grpcCodeByStatus[status]
	return code, ok
}
