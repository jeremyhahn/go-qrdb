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

package node

import (
	"errors"
	"testing"
)

func TestNodeErrors_MessagesAndCauses(t *testing.T) {
	cause := errors.New("boom")
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"storage":         {&StorageCreateError{Backend: "pebble", Err: cause}, "node: failed to create storage backend pebble: boom"},
		"engine":          {&EngineCreateError{Engine: "mvcc", Err: cause}, "node: failed to create engine mvcc: boom"},
		"services":        {&ServicesCreateError{Err: cause}, "node: failed to build services: boom"},
		"server":          {&ServerCreateError{Err: cause}, "node: failed to start server: boom"},
		"host":            {&HostCreateError{Err: cause}, "node: failed to create clustered host: boom"},
		"config":          {&ConfigError{Field: "raft.data_dir", Message: "is required", Err: cause}, "node: config error: raft.data_dir is required"},
		"client":          {&ClientCreateError{Err: cause}, "node: failed to create SDK client: boom"},
		"unseal":          {&UnsealError{Err: cause}, "node: failed to unseal barrier: boom"},
		"login":           {&LoginWiringError{Err: cause}, "node: failed to wire unified-login ceremony: boom"},
		"auth factor":     {&AuthFactorWiringError{Err: cause}, "node: failed to wire AuthFactorService from FactorRegistry: boom"},
		"pending cert":    {&PendingAdminCertWriteError{Err: cause}, "node: failed to record the pending admin certificate: boom"},
		"tenant infra":    {&TenantInfraError{Err: cause}, "node: failed to build tenant infrastructure: boom"},
		"abac":            {&ABACWiringError{Err: cause}, "node: failed to wire ABAC authorization decorator: boom"},
		"auth middleware": {&AuthMiddlewareWiringError{Err: cause}, "node: failed to wire auth middleware for the multi-protocol server: boom"},
		"cluster join":    {&ClusterJoinError{Err: cause}, "node: cluster join: boom"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Errorf("Error() = %q, want %q", got, tc.want)
			}
			if !errors.Is(tc.err, cause) {
				t.Errorf("errors.Is(%T, cause) = false; the cause must unwrap", tc.err)
			}
		})
	}
}
