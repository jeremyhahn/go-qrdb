//go:build timeseries

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
	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"
)

// NewTimeSeriesRegularCreateFunc returns an sm.CreateFunc that creates
// TimeSeriesStateMachine instances in standard (serialized) mode.
func NewTimeSeriesRegularCreateFunc(
	storageFactory func(shardID, replicaID uint64) *timeseries.Storage,
	log *logger.Logger,
) sm.CreateFunc {
	return sm.NewCreateFunc(func(shardID, replicaID uint64) sm.StateMachine {
		storage := storageFactory(shardID, replicaID)
		tsm, err := NewTimeSeriesStateMachine(storage, shardID, nil, log)
		if err != nil {
			panic("statemachine: failed to create timeseries SM: " + err.Error())
		}
		return NewTimeSeriesStandardAdapter(tsm)
	})
}
