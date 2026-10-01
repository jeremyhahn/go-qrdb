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

package grpc

import (
	"errors"
	"testing"

	"google.golang.org/grpc/codes"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// A create refused a provisioning permission (databases:create_raft,
// databases:create_volume) answers as the refusal's own class, as the entry
// check's refusal would: a missing grant is permission denied, and an audit
// sink that could not record the refusal is unavailable -- never an internal
// error.
func TestProvisioningDenied_AnswersAsTheRefusal(t *testing.T) {
	denied := &service.ProvisioningDeniedError{TenantID: "acme", Database: "db", Permission: permission.DatabasesCreateRaft, Err: auth.ErrUnauthorized}
	unrecorded := &service.ProvisioningDeniedError{TenantID: "acme", Database: "db", Permission: permission.DatabasesCreateVolume,
		Err: &audit.GateError{Component: "authorize", Err: errors.New("sink down")}}
	if got := ServiceErrorCode(denied); got != codes.PermissionDenied {
		t.Errorf("missing grant: status = %v, want codes.PermissionDenied", got)
	}
	if got := ServiceErrorCode(unrecorded); got != codes.Unavailable {
		t.Errorf("unrecorded refusal: status = %v, want codes.Unavailable", got)
	}
}
