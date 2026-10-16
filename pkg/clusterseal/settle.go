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

package clusterseal

import (
	"context"
	"time"
)

// settle runs op until it succeeds, fails with an error Config.Retryable does
// not call transient, ctx ends, or deadline passes, and returns op's last
// error. op runs at least once. Every step of one Seal or Clear settles
// against the same deadline, so a request waits out an election once rather
// than once per read and write.
func (m *Monitor) settle(ctx context.Context, deadline time.Time, op func() error) error {
	clock := m.cfg.Clock
	for {
		err := op()
		if err == nil || m.cfg.Retryable == nil || !m.cfg.Retryable(err) || ctx.Err() != nil {
			return err
		}
		remaining := deadline.Sub(clock.Now())
		if remaining <= 0 {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-clock.After(min(settlePoll, remaining)):
		}
	}
}

// settleDeadline starts the one SettleBudget of a Seal or Clear.
func (m *Monitor) settleDeadline() time.Time {
	return m.cfg.Clock.Now().Add(SettleBudget)
}

// loadSettled is load, settled against deadline.
func (m *Monitor) loadSettled(ctx context.Context, deadline time.Time, b Barrier) (Record, []byte, bool, error) {
	var (
		rec   Record
		raw   []byte
		found bool
	)
	err := m.settle(ctx, deadline, func() (err error) {
		rec, raw, found, err = m.load(ctx, b)
		return err
	})
	return rec, raw, found, err
}

// writeSettled is write, settled against deadline. A write that timed out may
// still have committed; its retry then finds the record changed and reports
// not written, and the caller's conflict path reads the record back and finds
// its own write there.
func (m *Monitor) writeSettled(ctx context.Context, deadline time.Time, next Record, raw []byte, found bool) (bool, error) {
	var ok bool
	err := m.settle(ctx, deadline, func() (err error) {
		ok, err = m.write(ctx, next, raw, found)
		return err
	})
	return ok, err
}
