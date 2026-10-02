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

import (
	"testing"

	"google.golang.org/grpc/codes"
)

// allGRPCCodes is gRPC's closed set, written out: a literal and not a range
// over 0..16, so a gRPC that grew an eighteenth code fails the bijection
// rather than passing it.
var allGRPCCodes = []codes.Code{
	codes.OK, codes.Canceled, codes.Unknown, codes.InvalidArgument,
	codes.DeadlineExceeded, codes.NotFound, codes.AlreadyExists,
	codes.PermissionDenied, codes.ResourceExhausted, codes.FailedPrecondition,
	codes.Aborted, codes.OutOfRange, codes.Unimplemented, codes.Internal,
	codes.Unavailable, codes.DataLoss, codes.Unauthenticated,
}

// TestStatusByGRPCCode_IsABijection is the claim the vocabulary decision
// rests on: this wire can express every condition gRPC can, and no two of
// gRPC's conditions collapse into one status here.
//
// It matters because the QUIC server PROJECTS gRPC services onto its frames
// (pkg/api/quic/modules.go). A code with no row degrades a module's answer to StatusError,
// and two codes sharing a row make two different module answers the same
// frame — both of which are the defect go-qrdb#437 exists to remove, arriving
// through the one path that cannot be fixed by adding a clause to a handler.
func TestStatusByGRPCCode_IsABijection(t *testing.T) {
	if len(statusByGRPCCode) != len(allGRPCCodes) {
		t.Errorf("correspondence has %d rows for %d gRPC codes",
			len(statusByGRPCCode), len(allGRPCCodes))
	}

	seen := make(map[uint16]codes.Code, len(statusByGRPCCode))
	for _, code := range allGRPCCodes {
		st, ok := statusByGRPCCode[code]
		if !ok {
			t.Errorf("codes.%s has no wire status: a module answering it would "+
				"reach a QUIC caller as the generic StatusError", code)
			continue
		}
		if !StatusDeclared(st) {
			t.Errorf("codes.%s maps to status %d, which the wire package does not declare", code, st)
		}
		if prev, dup := seen[st]; dup {
			t.Errorf("codes.%s and codes.%s both map to %s: two conditions "+
				"sharing one status cannot be told apart by a caller",
				prev, code, StatusName(st))
		}
		seen[st] = code
	}
}

// TestStatusByGRPCCode_CoversEveryDeclaredStatus is the bijection's other
// direction: a status the correspondence never produces is a status no
// projected module can ever reach, which would make it dead vocabulary.
func TestStatusByGRPCCode_CoversEveryDeclaredStatus(t *testing.T) {
	produced := make(map[uint16]bool, len(statusByGRPCCode))
	for _, st := range statusByGRPCCode {
		produced[st] = true
	}
	for st := uint16(0); StatusDeclared(st); st++ {
		if !produced[st] {
			t.Errorf("%s (%d) is declared but no gRPC code maps to it", StatusName(st), st)
		}
	}
}

// TestGRPCCodeForStatus_InvertsTheCorrespondence: every declared status reads
// back to the code that maps to it, and an undeclared one to nothing.
func TestGRPCCodeForStatus_InvertsTheCorrespondence(t *testing.T) {
	for _, code := range allGRPCCodes {
		st, ok := StatusForGRPCCode(code)
		if !ok {
			t.Fatalf("codes.%s has no status", code)
		}
		if back, ok := GRPCCodeForStatus(st); !ok || back != code {
			t.Errorf("GRPCCodeForStatus(%s) = %s, %v; want %s", StatusName(st), back, ok, code)
		}
	}
	if _, ok := GRPCCodeForStatus(StatusUnknown + 1); ok {
		t.Error("an undeclared status has a gRPC code")
	}
	if _, ok := StatusForGRPCCode(codes.Code(99)); ok {
		t.Error("a code gRPC does not define has a status")
	}
}
