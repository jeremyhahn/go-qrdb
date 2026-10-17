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

package sdk

import (
	"context"
	"errors"
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// electionRegistrationError is the error a registration proposed into a
// system-shard election reached the boot with (go-qrdb CI run 55243): the
// embedded transport's wrapper over a propose a leader stepping down had
// already accepted.
func electionRegistrationError() error {
	return &transport.RequestError{Method: "embedded", Err: &service.ProposeError{
		Err: &quicraft.MaybeAppliedError{Err: &quicraft.NotLeaderError{ShardID: 1}},
	}}
}

func TestSettleRegistration_WaitsOutElection(t *testing.T) {
	calls := 0
	err := settleRegistration(context.Background(), func(context.Context) error {
		calls++
		if calls <= 2 {
			return electionRegistrationError()
		}
		return nil
	})
	if err != nil {
		t.Fatalf("settleRegistration = %v, want nil", err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 2 elections and the write", calls)
	}
}

func TestSettleRegistration_RefusalIsFinal(t *testing.T) {
	refusal := &transport.CapabilityUnsupportedError{Message: "statemachine: lease not configured"}
	calls := 0
	err := settleRegistration(context.Background(), func(context.Context) error {
		calls++
		return refusal
	})
	if !errors.Is(err, refusal) || calls != 1 {
		t.Fatalf("settleRegistration = %v after %d calls, want %v after 1", err, calls, refusal)
	}
}

func TestSettleRegistration_EndsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	err := settleRegistration(ctx, func(context.Context) error {
		calls++
		cancel()
		return electionRegistrationError()
	})
	if !quicraft.MaybeApplied(err) || calls != 1 {
		t.Fatalf("settleRegistration = %v after %d calls, want the election's error after 1", err, calls)
	}
}
