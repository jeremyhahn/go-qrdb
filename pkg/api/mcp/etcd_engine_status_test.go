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

package mcp

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
)

// etcd enabled on a database whose engine is not kv is an invalid request,
// answered as such on every transport rather than as an internal error.
func TestEtcdEngineUnsupported_IsInvalidArgument(t *testing.T) {
	err := &database.EtcdEngineUnsupportedError{Name: "metrics", Engine: database.EngineTimeSeries}
	if got := ServiceErrorCode(err); got != wire.CodeInvalidArgument {
		t.Errorf("status = %v, want wire.CodeInvalidArgument", got)
	}
}
