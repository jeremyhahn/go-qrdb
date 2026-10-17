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

package statemachine

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

// TestRefusalKind_NamesEverySubsystemRefusal pins the table that replaced the
// one catch-all encoding (go-qrdb#557), wrapped as a subsystem would return
// it, so a rule that only matched a bare value would be caught.
func TestRefusalKind_NamesEverySubsystemRefusal(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want protocol.AppErrorKind
	}{
		{&timeseries.NotCompiledError{}, protocol.AppErrorCapability},
		{&ServiceNotConfiguredError{Service: "lease"}, protocol.AppErrorCapability},
		{&lease.InvalidTTLError{TTL: 0}, protocol.AppErrorInvalid},
		{&lease.ExistsError{ID: 7}, protocol.AppErrorConflict},
		{&lease.LessorClosedError{}, protocol.AppErrorUnavailable},
		{&timeseries.InvalidMetricError{}, protocol.AppErrorInvalid},
		{&timeseries.InvalidTimestampError{}, protocol.AppErrorInvalid},
		{&timeseries.InvalidTimeRangeError{}, protocol.AppErrorInvalid},
		{&timeseries.ValidationError{}, protocol.AppErrorInvalid},
		{&timeseries.InvalidAggFuncError{}, protocol.AppErrorInvalid},
		{&timeseries.InvalidBatchPointError{}, protocol.AppErrorInvalid},
		{&timeseries.ClosedError{}, protocol.AppErrorUnavailable},
		{&vlog.InvalidLogNameError{}, protocol.AppErrorInvalid},
		{&vlog.InvalidDescriptorError{}, protocol.AppErrorInvalid},
		{&vlog.LogNotFoundError{}, protocol.AppErrorInvalid},
		{&vlog.AppendBatchError{}, protocol.AppErrorInvalid},
		{&vlog.SizeOutOfRangeError{}, protocol.AppErrorInvalid},
		{&vlog.IndexOutOfRangeError{}, protocol.AppErrorInvalid},
		{&vlog.CheckpointRootMismatchError{}, protocol.AppErrorInvalid},
		{&vlog.EmptyCheckpointError{}, protocol.AppErrorInvalid},
		{&vlog.ExpireNotAllowedError{}, protocol.AppErrorInvalid},
		{&vlog.LogExistsError{}, protocol.AppErrorConflict},
		{&vlog.CheckpointExistsError{}, protocol.AppErrorConflict},
		{errors.New("pebble: write failed"), protocol.AppErrorFault},
	} {
		if got := RefusalKind(fmt.Errorf("apply: %w", tc.err)); got != tc.want {
			t.Errorf("RefusalKind(%T) = %d, want %d", tc.err, got, tc.want)
		}
	}
}

// TestRefusalResult_EncodesTheKindAndMessage: the payload a handler returns
// decodes to exactly the kind RefusalKind assigns and the error's own text.
func TestRefusalResult_EncodesTheKindAndMessage(t *testing.T) {
	err := &lease.InvalidTTLError{TTL: -1}
	msg, kind, ok := protocol.DecodeAppErrorKind(RefusalResult(err).Data)
	if !ok || kind != protocol.AppErrorInvalid || msg != err.Error() {
		t.Fatalf("decoded %q, %d, %v; want %q, AppErrorInvalid, true", msg, kind, ok, err.Error())
	}
	msg, kind, ok = protocol.DecodeAppErrorKind(CapabilityResult("lessor not configured").Data)
	if !ok || kind != protocol.AppErrorCapability || msg != "lessor not configured" {
		t.Fatalf("CapabilityResult decoded %q, %d, %v", msg, kind, ok)
	}
}
