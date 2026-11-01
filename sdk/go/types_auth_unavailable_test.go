// Copyright (c) 2026 Jeremy Hahn
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

package sdk_test

import (
	"errors"
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

// The alias is the middleware's own type, so a consumer's errors.As matches
// what the middleware returns.
var _ *authmw.StoreUnavailableError = (*sdk.StoreUnavailableError)(nil)

func TestSDK_StoreUnavailableError_MatchesTheMiddlewaresRefusal(t *testing.T) {
	cause := errors.New("raft not ready")
	var err error = &authmw.AuthChainError{Err: &authmw.StoreUnavailableError{Err: cause}}

	var su *sdk.StoreUnavailableError
	if !errors.As(err, &su) {
		t.Fatalf("errors.As(%v, *sdk.StoreUnavailableError) = false, want true", err)
	}
	if !errors.Is(su, cause) {
		t.Errorf("the matched refusal does not wrap its cause %v", cause)
	}
	if got := authmw.ClassifyAuthError(su); got != authmw.AuthFailureUnavailable {
		t.Errorf("ClassifyAuthError = %v, want AuthFailureUnavailable", got)
	}
}

func TestSDK_StoreUnavailableError_DoesNotMatchACredentialRefusal(t *testing.T) {
	for _, err := range []error{
		&authmw.AuthChainError{Err: errors.New("no peer cert")},
		&authmw.UserDisabledError{Username: "bob", TenantID: "t1", Lifecycle: "suspended"},
	} {
		var su *sdk.StoreUnavailableError
		if errors.As(err, &su) {
			t.Errorf("errors.As(%v, *sdk.StoreUnavailableError) = true, want false", err)
		}
	}
}

func TestSDK_IsLeadershipTransient_RecognisesAnElection(t *testing.T) {
	for name, cause := range map[string]error{
		"NotLeaderError":        &sdk.NotLeaderError{ShardID: 1},
		"ErrTimeout":            quicraft.ErrTimeout,
		"ErrAborted":            quicraft.ErrAborted,
		"ErrNotLeader":          quicraft.ErrNotLeader,
		"ErrShardNotReady":      quicraft.ErrShardNotReady,
		"ErrReadIndexNotReady":  quicraft.ErrReadIndexNotReady,
		"ErrTransferInProgress": quicraft.ErrTransferInProgress,
	} {
		t.Run(name, func(t *testing.T) {
			// Wrapped the way an auth fallback's tenant read returns it.
			err := &authmw.AuthChainError{Err: &tenant.StoreError{Op: "get", Err: cause}}
			if !sdk.IsLeadershipTransient(err) {
				t.Fatalf("IsLeadershipTransient(%v) = false, want true", err)
			}
		})
	}
}

func TestSDK_IsLeadershipTransient_RejectsEverythingElse(t *testing.T) {
	for name, err := range map[string]error{
		"nil":                  nil,
		"credential refusal":   &authmw.AuthChainError{Err: errors.New("token expired")},
		"store unavailable":    &authmw.StoreUnavailableError{Err: errors.New("raft not ready")},
		"sealed tenant":        tenant.ErrTenantSealed,
		"unrelated tenant get": &tenant.StoreError{Op: "get", Err: errors.New("corrupt record")},
	} {
		t.Run(name, func(t *testing.T) {
			if sdk.IsLeadershipTransient(err) {
				t.Fatalf("IsLeadershipTransient(%v) = true, want false", err)
			}
		})
	}
}
