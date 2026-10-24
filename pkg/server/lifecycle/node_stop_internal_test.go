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

package lifecycle

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/dek"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestNode_StopRunsEveryCloserLIFOAndReturnsTheFirstFailure: Stop tears down
// in reverse registration order, keeps going past a failing closer so nothing
// after it leaks, and reports the FIRST failure it met -- the one from the
// most recently registered closer -- rather than the last.
func TestNode_StopRunsEveryCloserLIFOAndReturnsTheFirstFailure(t *testing.T) {
	errFirstRegistered := errors.New("first-registered closer failed")
	errLastRegistered := errors.New("last-registered closer failed")

	var order []string
	n := &Node{}
	n.addCloser(func() error { order = append(order, "a"); return errFirstRegistered })
	n.addCloser(func() error { order = append(order, "b"); return nil })
	n.addCloser(func() error { order = append(order, "c"); return errLastRegistered })

	err := n.Stop(context.Background())
	if !errors.Is(err, errLastRegistered) {
		t.Fatalf("Stop() = %v, want the first failure met (%v)", err, errLastRegistered)
	}
	if want := []string{"c", "b", "a"}; !slices.Equal(order, want) {
		t.Fatalf("closer order = %v, want %v (LIFO, every closer run)", order, want)
	}
}

// TestBuildDEKService_RefusesANilResolver: without a store resolver there is
// nowhere to keep a tenant's DEK, so the service is refused with a typed
// error rather than built to fail on first use.
func TestBuildDEKService_RefusesANilResolver(t *testing.T) {
	svc, err := buildDEKService(nil, nil, nil, nil, "")
	if svc != nil {
		t.Fatalf("buildDEKService(nil) returned a service: %v", svc)
	}
	var target *DEKServiceError
	if !errors.As(err, &target) {
		t.Fatalf("buildDEKService(nil) error = %T (%v), want *DEKServiceError", err, err)
	}
	var invalid *dek.InvalidInputError
	if !errors.As(err, &invalid) {
		t.Fatalf("DEKServiceError does not wrap *dek.InvalidInputError: %v", err)
	}
}

// TestBuildPolicyService_RefusesANilResolver: without a store resolver there
// is nowhere to keep a tenant's policies, so the service is refused with a
// typed error rather than built to fail on first use.
func TestBuildPolicyService_RefusesANilResolver(t *testing.T) {
	svc, err := buildPolicyService(nil)
	if svc != nil {
		t.Fatalf("buildPolicyService(nil) returned a service: %v", svc)
	}
	var target *PolicyServiceError
	if !errors.As(err, &target) {
		t.Fatalf("buildPolicyService(nil) error = %T (%v), want *PolicyServiceError", err, err)
	}
	var unconfigured *service.SubsystemNotConfiguredError
	if !errors.As(err, &unconfigured) {
		t.Fatalf("PolicyServiceError does not wrap *service.SubsystemNotConfiguredError: %v", err)
	}
}
