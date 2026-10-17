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

package service

import (
	"errors"
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
)

// maybeAppliedNotLeader is what go-quicraft delivers to a proposal or config
// change already in the log when its leader steps down: a not-leader error
// naming no leader, wrapped because the entry may still commit under the next
// leader.
func maybeAppliedNotLeader() error {
	return &quicraft.MaybeAppliedError{Err: &quicraft.NotLeaderError{ShardID: 1}}
}

// A not-leader refusal is translated to *NotLeaderError, which marks the write
// proven not stored and which REST forwards to the leader. A maybe-applied one
// is neither: its outcome is unknown, so it stays an outcome-unknown failure
// that keeps go-quicraft's marker for every caller further up.
func TestTranslators_MaybeAppliedNotLeaderIsNotARefusal(t *testing.T) {
	admin := NewAdminService(&mockHost{}).(*AdminService)
	for name, translate := range map[string]func(error) error{
		"propose": translateProposeError,
		"membership": func(err error) error {
			return admin.translateMembershipError("add", err)
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := translate(maybeAppliedNotLeader())

			var nle *NotLeaderError
			if errors.As(err, &nle) {
				t.Fatalf("translated to %T; a maybe-applied failure is not a not-leader refusal", nle)
			}
			if errors.Is(err, ErrNotStored) {
				t.Fatal("marked ErrNotStored; the entry may still commit")
			}
			if !quicraft.MaybeApplied(err) {
				t.Fatalf("%v: lost the maybe-applied marker", err)
			}
			if !LeadershipTransient(err) {
				t.Fatalf("%v: a leader stepping down is still a leadership transient", err)
			}
		})
	}
}

// The outcome of a maybe-applied proposal is unknown, which is what
// ProposeError reports.
func TestTranslateProposeError_MaybeAppliedIsOutcomeUnknown(t *testing.T) {
	err := translateProposeError(maybeAppliedNotLeader())

	var pe *ProposeError
	if !errors.As(err, &pe) {
		t.Fatalf("got %T, want *ProposeError", err)
	}
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatal("not marked ErrOutcomeUnknown")
	}
}
