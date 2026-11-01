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

// Package feature is the identity of a cluster feature: its name, Feature,
// and the one refusal of an operation that needs a feature the cluster has
// not yet activated, NotActiveError.
//
// It is a leaf. The gate that decides whether a feature is enabled or
// activated, and the names of the features this binary implements, live in
// pkg/cluster/featuregate; a package that only names a feature or refuses on
// one (pkg/shard's claimer, the service layer) imports this package and not
// the gate's membership machinery.
package feature

import "strconv"

// Feature names a cluster feature: a command or behaviour every replica of
// a group must understand before any replica may be sent it.
type Feature string

// NotActiveError refuses an operation that depends on a cluster feature the
// cluster has not yet activated: until every node -- for a group's command,
// every replica of the group -- runs a binary that implements the feature,
// using it could let one node act on what another cannot apply. Nothing was
// done. It is the one refusal for every feature-gated operation, and it is
// transient: the operation succeeds once activation completes, so it is
// classified Unavailable on every transport.
type NotActiveError struct {
	// Feature is the feature the operation needs.
	Feature Feature
	// ShardID is the group whose replicas must all support the feature, or
	// zero for a feature the operation needs cluster-wide.
	ShardID uint64
}

// Error implements error.
func (e *NotActiveError) Error() string {
	msg := "featuregate: cluster feature " + strconv.Quote(string(e.Feature)) + " is not active yet"
	if e.ShardID != 0 {
		msg += " on shard " + strconv.FormatUint(e.ShardID, 10)
	}
	return msg + ": every node must run a release that supports it"
}
