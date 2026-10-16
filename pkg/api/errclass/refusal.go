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

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
)

// refusalEncoder returns the structured refusal of the errors it knows.
type refusalEncoder func(err error) (reason string, md map[string]string, ok bool)

// refusals are the structured refusals a server puts on the wire, in the
// order they are tried. Every reason is unique within replicaforward's
// ErrorDomain, so the order only matters for an error that wraps two.
var refusals = []refusalEncoder{
	replicaforward.Refusal,
	factorConditionRefusal,
}

// Refusal returns the structured refusal err carries -- a reason and its
// metadata, in replicaforward.ErrorDomain -- for a transport to put on the
// wire beside the message, so a forwarding node or a client rebuilds the
// typed error without parsing text. An error that carries none is not one.
func Refusal(err error) (reason string, md map[string]string, ok bool) {
	for _, encode := range refusals {
		if reason, md, ok = encode(err); ok {
			return reason, md, true
		}
	}
	return "", nil, false
}

// factorConditionRefusal encodes a conditional factor add refused because
// its condition did not hold.
func factorConditionRefusal(err error) (string, map[string]string, bool) {
	if e, ok := errors.AsType[*auth.FactorConditionFailedError](err); ok {
		return auth.ReasonFactorConditionFailed, e.Metadata(), true
	}
	return "", nil, false
}
