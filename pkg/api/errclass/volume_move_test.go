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
	"errors"
	"fmt"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// TestClassify_VolumeMoveErrors pins the class of every tenant-move error,
// including MoveStepError, whose class follows its cause rather than the
// cause's own row.
func TestClassify_VolumeMoveErrors(t *testing.T) {
	unavailable := &volume.PoolUnavailableError{Pool: "p", NodeID: "n1"}
	sealed := &volume.NodeSealedError{Op: "move step"}
	mismatch := &volume.ManifestMismatchError{Path: "a", Reason: "digest"}
	conflict := &volume.MoveConflictError{TenantID: "t", Reason: "busy"}

	cases := []struct {
		name string
		err  error
		want Class
	}{
		{"MoveConflictError", conflict, FailedPrecondition},
		{"TenantMovingReplicaError", &volume.TenantMovingReplicaError{TenantID: "t", FromPool: "a", ToPool: "b", State: "copying"}, FailedPrecondition},
		{"MoveToUnencryptedPoolError", &volume.MoveToUnencryptedPoolError{TenantID: "t", FromPool: "a", ToPool: "b"}, FailedPrecondition},
		{"MoveEncryptionDropUnauditedError", &volume.MoveEncryptionDropUnauditedError{TenantID: "t", FromPool: "a", ToPool: "b"}, FailedPrecondition},
		{"MoveNotFoundError", &volume.MoveNotFoundError{TenantID: "t"}, NotFound},
		{"InvalidMoveRequestError", &volume.InvalidMoveRequestError{Reason: "empty tenant"}, InvalidArgument},
		{"MoveJournalConflictError", &volume.MoveJournalConflictError{TenantID: "t"}, Aborted},
		{"ManifestMismatchError", mismatch, Internal},
		{"NodeSealedError", sealed, Unavailable},
		{"MoveStepError/PoolUnavailable", &volume.MoveStepError{Phase: "copy", Err: unavailable}, Unavailable},
		{"MoveStepError/NodeSealed", &volume.MoveStepError{Phase: "copy", Err: sealed}, Unavailable},
		{"MoveStepError/ManifestMismatch", &volume.MoveStepError{Phase: "verify", Err: mismatch}, Internal},
		{"MoveStepError/plain", &volume.MoveStepError{Phase: "copy", Err: errors.New("disk")}, Internal},
		{"MoveStepError/MoveConflict", &volume.MoveStepError{Phase: "abort", Err: conflict}, Internal},
		{"wrapped MoveStepError/PoolUnavailable", fmt.Errorf("run step: %w", &volume.MoveStepError{Phase: "copy", Err: unavailable}), Unavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.err); got != tc.want {
				t.Fatalf("Classify(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
