// Copyright 2026 Jeremy Hahn
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

package unix

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestUnixRT_UpdatePoolNilRequest pins that a nil UpdatePool request is
// refused with a typed error rather than dereferenced.
func TestUnixRT_UpdatePoolNilRequest(t *testing.T) {
	tr, cleanup := startJoinServer(t, &joinAdminSvc{mockAdminService: newMockAdminService()})
	defer cleanup()

	_, err := tr.UpdatePool(context.Background(), nil)
	var nre *transport.NilRequestError
	var re *transport.RequestError
	if !errors.As(err, &re) || re.Method != "UpdatePool" || !errors.As(err, &nre) {
		t.Fatalf("UpdatePool(nil) = %T %v, want RequestError wrapping NilRequestError", err, err)
	}
}

// TestUnixRT_UpdatePoolNilRequestClosed pins that a closed transport reports
// ClosedError ahead of the nil-request refusal.
func TestUnixRT_UpdatePoolNilRequestClosed(t *testing.T) {
	tr, cleanup := startJoinServer(t, &joinAdminSvc{mockAdminService: newMockAdminService()})
	defer cleanup()
	tr.closed.Store(true)

	var ce *transport.ClosedError
	if _, err := tr.UpdatePool(context.Background(), nil); !errors.As(err, &ce) {
		t.Fatalf("UpdatePool(nil) on a closed transport = %T %v, want ClosedError", err, err)
	}
}
