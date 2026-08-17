//go:build qrdb_ts

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
	"github.com/jeremyhahn/go-quicraft/pkg/sm"
)

// TimeSeriesStandardAdapter wraps TimeSeriesStateMachine to implement sm.StateMachine.
// This is the default adapter used when the TimeSeries state machine runs in
// standard (serialized) mode within QuicRaft.
type TimeSeriesStandardAdapter struct {
	*TimeSeriesStateMachine
}

// NewTimeSeriesStandardAdapter creates a standard adapter around a TimeSeriesStateMachine.
func NewTimeSeriesStandardAdapter(tsm *TimeSeriesStateMachine) *TimeSeriesStandardAdapter {
	return &TimeSeriesStandardAdapter{TimeSeriesStateMachine: tsm}
}

// Compile-time assertion that TimeSeriesStandardAdapter implements sm.StateMachine.
var _ sm.StateMachine = (*TimeSeriesStandardAdapter)(nil)
