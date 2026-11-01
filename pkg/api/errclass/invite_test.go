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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/invite"
)

// TestClassify_InviteReissueConflict: a re-invitation that lost the race to
// another is an aborted write the caller may retry, not the internal error an
// unclassified error gets -- bare or wrapped.
func TestClassify_InviteReissueConflict(t *testing.T) {
	for _, err := range []error{
		invite.ErrReissueConflict,
		errors.Join(errors.New("reinvite"), invite.ErrReissueConflict),
	} {
		if got := Classify(err); got != Aborted {
			t.Errorf("Classify(%v) = %v, want Aborted", err, got)
		}
	}
}
