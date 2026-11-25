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

import "testing"

func TestNumberedSampleError_ErrorAndNumber(t *testing.T) {
	e := &numberedSampleError{number: 1146, msg: "table does not exist"}
	if e.Error() != "table does not exist" {
		t.Fatalf("Error() = %q", e.Error())
	}
	if e.Number() != 1146 {
		t.Fatalf("Number() = %d", e.Number())
	}
}
