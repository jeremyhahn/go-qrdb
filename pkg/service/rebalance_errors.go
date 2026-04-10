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

package service

import "strconv"

// ShardStatsError is returned when querying shard statistics fails.
type ShardStatsError struct {
	ShardID uint64
	Err     error
}

// Error returns a human-readable description of the shard stats failure.
func (e *ShardStatsError) Error() string {
	return "service: get shard stats " + strconv.FormatUint(e.ShardID, 10) + " failed: " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *ShardStatsError) Unwrap() error {
	return e.Err
}

// RebalancePlanError is returned when generating a rebalance plan fails.
type RebalancePlanError struct {
	Err error
}

// Error returns a human-readable description of the rebalance plan failure.
func (e *RebalancePlanError) Error() string {
	return "service: create rebalance plan failed: " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *RebalancePlanError) Unwrap() error {
	return e.Err
}

// RebalanceExecuteError is returned when executing a rebalance plan fails.
type RebalanceExecuteError struct {
	Err error
}

// Error returns a human-readable description of the rebalance execution failure.
func (e *RebalanceExecuteError) Error() string {
	return "service: execute rebalance failed: " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *RebalanceExecuteError) Unwrap() error {
	return e.Err
}

// ImbalanceReportError is returned when generating an imbalance report fails.
type ImbalanceReportError struct {
	Err error
}

// Error returns a human-readable description of the imbalance report failure.
func (e *ImbalanceReportError) Error() string {
	return "service: get imbalance report failed: " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *ImbalanceReportError) Unwrap() error {
	return e.Err
}
