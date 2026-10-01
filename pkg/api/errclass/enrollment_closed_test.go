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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
)

// TestClassify_EnrollmentClosed: an anonymous registration after enrollment
// closed is a permission refusal on every transport, matching the
// sealed-window gate's own answer, not the internal error an unclassified
// error gets.
func TestClassify_EnrollmentClosed(t *testing.T) {
	if got := Classify(ceremony.ErrEnrollmentClosed); got != PermissionDenied {
		t.Fatalf("Classify(ErrEnrollmentClosed) = %v, want PermissionDenied", got)
	}
}
