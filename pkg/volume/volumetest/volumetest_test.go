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

package volumetest

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// recordingTB captures Errorf calls so a test can assert the double reported
// an unscripted call without failing itself.
type recordingTB struct {
	testing.TB
	errors int
}

func (r *recordingTB) Helper() {}

func (r *recordingTB) Errorf(string, ...any) { r.errors++ }

// calls invokes every pool method of svc and returns each method's error.
func calls(svc *Service) map[string]error {
	ctx := context.Background()
	_, listErr := svc.ListPools(ctx)
	_, getErr := svc.GetPool(ctx, "p")
	_, createErr := svc.CreatePool(ctx, volume.CreatePoolRequest{})
	_, updateErr := svc.UpdatePool(ctx, "p", volume.UpdatePoolRequest{})
	_, assignErr := svc.ListAssignments(ctx, "p", "", 0)
	return map[string]error{
		"ListPools":       listErr,
		"GetPool":         getErr,
		"CreatePool":      createErr,
		"UpdatePool":      updateErr,
		"DeletePool":      svc.DeletePool(ctx, "p"),
		"ListAssignments": assignErr,
	}
}

func TestService_UnscriptedCallReportsAndReturnsTypedError(t *testing.T) {
	tb := &recordingTB{TB: t}
	got := calls(&Service{T: tb})
	for method, err := range got {
		var unexpected *UnexpectedCallError
		if !errors.As(err, &unexpected) || unexpected.Method != method {
			t.Errorf("%s: err = %v, want *UnexpectedCallError{%s}", method, err, method)
		}
	}
	if tb.errors != len(got) {
		t.Errorf("reported %d unexpected calls, want %d", tb.errors, len(got))
	}
}

func TestService_UnscriptedCallWithoutTB(t *testing.T) {
	for method, err := range calls(&Service{}) {
		var unexpected *UnexpectedCallError
		if !errors.As(err, &unexpected) {
			t.Errorf("%s: err = %v, want *UnexpectedCallError", method, err)
		}
	}
	if got := (&UnexpectedCallError{Method: "GetPool"}).Error(); got != "volumetest: unexpected call to GetPool" {
		t.Errorf("Error() = %q", got)
	}
}

func TestService_ScriptedCallsDelegate(t *testing.T) {
	boom := &volume.PoolNotFoundError{Pool: "p"}
	svc := &Service{
		T:                 t,
		ListPoolsFn:       func(context.Context) ([]volume.PoolView, error) { return nil, boom },
		GetPoolFn:         func(context.Context, string) (*volume.PoolDetail, error) { return nil, boom },
		CreatePoolFn:      func(context.Context, volume.CreatePoolRequest) (*volume.Pool, error) { return nil, boom },
		UpdatePoolFn:      func(context.Context, string, volume.UpdatePoolRequest) (*volume.Pool, error) { return nil, boom },
		DeletePoolFn:      func(context.Context, string) error { return boom },
		ListAssignmentsFn: func(context.Context, string, string, int) (*volume.AssignmentPage, error) { return nil, boom },
	}
	for method, err := range calls(svc) {
		if !errors.Is(err, boom) {
			t.Errorf("%s: err = %v, want the scripted error", method, err)
		}
	}
}

func TestService_MovesAreNotImplementedLikeTheCatalog(t *testing.T) {
	svc := &Service{T: t}
	var notImpl *volume.MoveNotImplementedError
	if _, err := svc.MoveTenant(context.Background(), "t1", "p"); !errors.As(err, &notImpl) {
		t.Errorf("MoveTenant err = %v, want *volume.MoveNotImplementedError", err)
	}
	if _, err := svc.GetMove(context.Background(), "t1"); !errors.As(err, &notImpl) {
		t.Errorf("GetMove err = %v, want *volume.MoveNotImplementedError", err)
	}
}
