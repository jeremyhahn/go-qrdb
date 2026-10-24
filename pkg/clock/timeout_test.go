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

package clock_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clock"
	"github.com/jeremyhahn/go-qrdb/pkg/clock/clocktest"
)

type timeoutKey struct{}

// TestWithTimeout_RealIsTheRuntimeTimeout proves Real, and a nil Clock, take
// context.WithTimeout's deadline on the wall clock.
func TestWithTimeout_RealIsTheRuntimeTimeout(t *testing.T) {
	for name, c := range map[string]clock.Clock{"real": clock.Real(), "nil": nil} {
		t.Run(name, func(t *testing.T) {
			before := time.Now()
			ctx, cancel := clock.WithTimeout(context.Background(), c, time.Hour)
			deadline, ok := ctx.Deadline()
			if !ok || deadline.Before(before.Add(time.Hour)) || deadline.After(time.Now().Add(time.Hour)) {
				t.Errorf("Deadline() = %v, %v; want an hour from now on the wall clock", deadline, ok)
			}
			cancel()
			if err := ctx.Err(); !errors.Is(err, context.Canceled) {
				t.Errorf("Err() after cancel = %v, want context.Canceled", err)
			}
		})
	}
}

// TestWithTimeout_FakeEndsOnlyWhenAdvancedPastTheDeadline proves the deadline
// passes when the fake is advanced to it and not before, and that the context
// and one derived from it then report context.DeadlineExceeded.
func TestWithTimeout_FakeEndsOnlyWhenAdvancedPastTheDeadline(t *testing.T) {
	f := clocktest.New()
	ctx, cancel := clock.WithTimeout(context.Background(), f, time.Second)
	defer cancel()
	child, childCancel := context.WithCancel(ctx)
	defer childCancel()

	if deadline, ok := ctx.Deadline(); !ok || !deadline.Equal(f.Now().Add(time.Second)) {
		t.Errorf("Deadline() = %v, %v; want a second from the fake's now", deadline, ok)
	}
	f.Advance(time.Second - time.Nanosecond)
	select {
	case <-ctx.Done():
		t.Fatal("context done before the fake reached its deadline")
	default:
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("Err() before the deadline = %v, want nil", err)
	}

	f.Advance(time.Nanosecond)
	<-ctx.Done()
	if err := ctx.Err(); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Err() = %v, want context.DeadlineExceeded", err)
	}
	if err := context.Cause(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("context.Cause = %v, want context.DeadlineExceeded", err)
	}
	<-child.Done()
	if err := child.Err(); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("derived Err() = %v, want context.DeadlineExceeded", err)
	}
}

// TestWithTimeout_FakeCancel proves cancel ends the context with
// context.Canceled, and that the deadline passing afterwards does not change
// it.
func TestWithTimeout_FakeCancel(t *testing.T) {
	f := clocktest.New()
	ctx, cancel := clock.WithTimeout(context.Background(), f, time.Second)
	cancel()
	<-ctx.Done()
	f.Advance(time.Second)
	cancel()
	if err := ctx.Err(); !errors.Is(err, context.Canceled) {
		t.Errorf("Err() = %v, want context.Canceled", err)
	}
}

// TestWithTimeout_FakeEndsWithItsParent proves the context ends with its
// parent's error when the parent ends first, and carries the parent's values.
func TestWithTimeout_FakeEndsWithItsParent(t *testing.T) {
	parent, parentCancel := context.WithCancel(context.WithValue(context.Background(), timeoutKey{}, "v"))
	ctx, cancel := clock.WithTimeout(parent, clocktest.New(), time.Second)
	defer cancel()
	if got := ctx.Value(timeoutKey{}); got != "v" {
		t.Errorf("Value() = %v, want the parent's value", got)
	}
	parentCancel()
	<-ctx.Done()
	if err := ctx.Err(); !errors.Is(err, context.Canceled) {
		t.Errorf("Err() = %v, want the parent's context.Canceled", err)
	}
}

// TestWithTimeout_FakeKeepsAnEarlierParentDeadline proves Deadline reports the
// parent's deadline when it comes first, as context.WithDeadline does.
func TestWithTimeout_FakeKeepsAnEarlierParentDeadline(t *testing.T) {
	f := clocktest.New()
	parent, parentCancel := clock.WithTimeout(context.Background(), f, time.Second)
	defer parentCancel()
	ctx, cancel := clock.WithTimeout(parent, f, time.Minute)
	defer cancel()
	if deadline, _ := ctx.Deadline(); !deadline.Equal(f.Now().Add(time.Second)) {
		t.Errorf("Deadline() = %v, want the parent's %v", deadline, f.Now().Add(time.Second))
	}
	f.Advance(time.Second)
	<-ctx.Done()
	if err := ctx.Err(); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Err() = %v, want the parent's context.DeadlineExceeded", err)
	}
}

// TestWithTimeout_FakeNonPositiveIsAlreadyPast proves a non-positive timeout
// returns a context that is already past its deadline.
func TestWithTimeout_FakeNonPositiveIsAlreadyPast(t *testing.T) {
	ctx, cancel := clock.WithTimeout(context.Background(), clocktest.New(), 0)
	defer cancel()
	select {
	case <-ctx.Done():
	default:
		t.Fatal("context not done for a zero timeout")
	}
	if err := ctx.Err(); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Err() = %v, want context.DeadlineExceeded", err)
	}
}
