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

package embedded

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// A transport built without a family's servicer reports a wiring defect, the
// same for every family: there is no longer a mode in which a family is
// absent by design.
func TestTransport_AbsentServiceIsAWiringDefect(t *testing.T) {
	tr := newExtTransport(t)
	var nse *NilServiceError
	if _, err := tr.LeaseGrant(t.Context(), 5); !errors.As(err, &nse) {
		t.Errorf("LeaseGrant: err = %T (%v), want *NilServiceError", err, err)
	}
	if _, err := tr.LockAcquire(t.Context(), &transport.LockAcquireRequest{Key: "k", HolderID: "h", TTL: 5}); !errors.As(err, &nse) {
		t.Errorf("LockAcquire: err = %T (%v), want *NilServiceError", err, err)
	}
	if _, err := tr.GetRevision(t.Context(), "k"); !errors.As(err, &nse) {
		t.Errorf("GetRevision: err = %T (%v), want *NilServiceError", err, err)
	}
}
