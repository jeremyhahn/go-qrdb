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

package lease

import "testing"

// nopWriter accepts every lease-table write and keeps none. It is for tests
// about the Lessor's in-memory behaviour; tests about the table itself use a
// real storage so what was written can be read back.
type nopWriter struct{}

var _ Writer = nopWriter{}

func (nopWriter) Set(_, _ []byte) error { return nil }
func (nopWriter) Delete(_ []byte) error { return nil }

// mustBind binds key to leaseID, failing the test on error.
func mustBind(t *testing.T, lr *Lessor, leaseID int64, key []byte) {
	t.Helper()
	if err := lr.Bind(nopWriter{}, key, leaseID); err != nil {
		t.Fatalf("Bind(%q, %d): %v", key, leaseID, err)
	}
}

// mustUnbind releases key from its lease, failing the test on error.
func mustUnbind(t *testing.T, lr *Lessor, key []byte) {
	t.Helper()
	if err := lr.Unbind(nopWriter{}, key); err != nil {
		t.Fatalf("Unbind(%q): %v", key, err)
	}
}
