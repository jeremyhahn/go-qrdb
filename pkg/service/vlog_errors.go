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

package service

import (
	"strconv"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
)

// LogFeatureNotActiveError is returned by a LogService write while a cluster
// feature the write's command needs (featuregate.VerifiableLog, and for a
// create or a key rotation featuregate.VerifiableLogKeys) is not yet active on
// every replica of the tenant's group: a replica that cannot apply a command
// must never be sent it.
type LogFeatureNotActiveError struct {
	TenantID string
	ShardID  uint64
	Feature  featuregate.Feature
}

func (e *LogFeatureNotActiveError) Error() string {
	return "service: verifiable log write not yet active for tenant " + strconv.Quote(e.TenantID) +
		" (shard " + strconv.FormatUint(e.ShardID, 10) + "): the cluster feature gate has not activated " +
		string(e.Feature) + " on every replica of this shard"
}
