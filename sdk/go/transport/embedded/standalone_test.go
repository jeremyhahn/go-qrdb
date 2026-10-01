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
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestMissingServiceStandaloneRefusesRaftOnlyFamiliesTyped: on a standalone
// transport each raft-only family is refused as unsupported and the message
// names the family and the remedy.
func TestMissingServiceStandaloneRefusesRaftOnlyFamiliesTyped(t *testing.T) {
	tr := &Transport{standalone: true}
	for service, family := range raftOnlyFamilies {
		var unsupported *transport.CapabilityUnsupportedError
		err := tr.missingService(service)
		if !errors.As(err, &unsupported) {
			t.Fatalf("%s: err = %T, want *transport.CapabilityUnsupportedError", service, err)
		}
		if !strings.Contains(err.Error(), family) || !strings.Contains(err.Error(), "requires mode: raft") {
			t.Errorf("%s: message %q does not name the family and the remedy", service, err)
		}
	}
}

// TestMissingServiceKeepsNilServiceErrorForWiringDefects: outside standalone
// mode, and for any service that is not raft-only, an absent service is a
// wiring defect and stays *NilServiceError.
func TestMissingServiceKeepsNilServiceErrorForWiringDefects(t *testing.T) {
	var nse *NilServiceError
	if err := (&Transport{}).missingService("LeaseServicer"); !errors.As(err, &nse) {
		t.Fatalf("raft mode: err = %T, want *NilServiceError", err)
	}
	if err := (&Transport{standalone: true}).missingService("BarrierServicer"); !errors.As(err, &nse) {
		t.Fatalf("standalone, non-raft-only service: err = %T, want *NilServiceError", err)
	}
}

// TestWithStandaloneModeRoutesTheRealMethods drives the transport's own lease
// and lock entry points, not just the helper.
func TestWithStandaloneModeRoutesTheRealMethods(t *testing.T) {
	tr := newExtTransport(t, WithStandaloneMode())
	var unsupported *transport.CapabilityUnsupportedError
	if _, err := tr.LeaseGrant(t.Context(), 5); !errors.As(err, &unsupported) {
		t.Errorf("LeaseGrant: err = %T (%v)", err, err)
	}
	if _, err := tr.LockAcquire(t.Context(), &transport.LockAcquireRequest{Key: "k", HolderID: "h", TTL: 5}); !errors.As(err, &unsupported) {
		t.Errorf("LockAcquire: err = %T (%v)", err, err)
	}
	if _, err := tr.GetRevision(t.Context(), "k"); !errors.As(err, &unsupported) {
		t.Errorf("GetRevision: err = %T (%v)", err, err)
	}
}
