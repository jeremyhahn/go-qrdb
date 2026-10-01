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
	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// raftOnlyFamilies maps the servicer of each raft-only service family to the
// family name. These are exactly the families a standalone server refuses
// (pkg/server's raftOnlyGatedRegistry): consensus is what makes them
// meaningful, and a standalone node has none.
var raftOnlyFamilies = map[string]string{
	"MVCCKVServicer": "mvcc",
	"WatchServicer":  "watch",
	"LeaseServicer":  "lease",
	"LockServicer":   "lock",
}

// WithStandaloneMode marks the transport as serving a raft-free composition.
// A call into a raft-only family then answers the same typed refusal the
// network transports give a standalone server —
// *transport.CapabilityUnsupportedError — instead of *NilServiceError, which
// stays reserved for a service that should have been wired and was not.
func WithStandaloneMode() Option {
	return func(t *Transport) { t.standalone = true }
}

// missingService is the error for a call into a service the transport was
// built without. On a standalone transport a raft-only family is absent by
// design and refused as unsupported; anything else is a wiring defect.
func (t *Transport) missingService(service string) error {
	if family, raftOnly := raftOnlyFamilies[service]; raftOnly && t.standalone {
		return &transport.CapabilityUnsupportedError{Message: family + " service: " + capabilities.RaftOnlyHint}
	}
	return &NilServiceError{Service: service}
}
