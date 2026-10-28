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
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
)

func TestLogFeatureNotActiveError_NamesTenantAndShard(t *testing.T) {
	msg := (&LogFeatureNotActiveError{TenantID: "acme", ShardID: 7, Feature: featuregate.VerifiableLog}).Error()
	for _, want := range []string{`"acme"`, "shard 7", "verifiable-log on"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Error() = %q, missing %q", msg, want)
		}
	}
}

func TestLogFeatureNotActiveError_NamesKeyFeature(t *testing.T) {
	msg := (&LogFeatureNotActiveError{TenantID: "acme", ShardID: 7, Feature: featuregate.VerifiableLogKeys}).Error()
	for _, want := range []string{`"acme"`, "shard 7", "verifiable-log-keys on"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Error() = %q, missing %q", msg, want)
		}
	}
}

func TestBarrierTenantLogNotSupportedError_NamesTenantAndBarrier(t *testing.T) {
	msg := (&BarrierTenantLogNotSupportedError{TenantID: "vault"}).Error()
	for _, want := range []string{`"vault"`, "barrier-isolated", "tenant's barrier"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Error() = %q, missing %q", msg, want)
		}
	}
}

func TestBarrierDatabaseLogNotSupportedError_NamesDatabaseAndBarrier(t *testing.T) {
	msg := (&BarrierDatabaseLogNotSupportedError{TenantID: "acme", Database: "secure"}).Error()
	for _, want := range []string{`"acme"`, `"secure"`, "barrier-enabled database", "database's barrier"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Error() = %q, missing %q", msg, want)
		}
	}
}

func TestLogFeatureNotActiveError_SystemTenant(t *testing.T) {
	msg := (&LogFeatureNotActiveError{}).Error()
	if !strings.Contains(msg, `""`) || !strings.Contains(msg, "shard 0") {
		t.Errorf("Error() = %q, want the empty tenant and shard 0 named", msg)
	}
}
