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

package service

import (
	"context"
	"time"
)

// SettleLeadership runs attempt until it answers with anything but a
// LeadershipTransient error, so a read issued while its shard elects a leader
// or moves one waits the election out instead of failing with it.
//
// The first attempt runs at once; each retry follows poll later, or sooner if
// less of budget remains. The wait ends when an attempt succeeds or fails for
// any other reason (that answer is returned as is), when ctx ends, or when
// budget has passed since the call began; the last attempt's transient error
// then stands, with the zero T. Each attempt bounds itself: SettleLeadership
// does not give one a deadline, and does not retry a cancelled request.
func SettleLeadership[T any](ctx context.Context, budget, poll time.Duration, attempt func() (T, error)) (T, error) {
	var zero T
	deadline := time.Now().Add(budget)
	for {
		v, err := attempt()
		if err == nil || !LeadershipTransient(err) || ctx.Err() != nil {
			return v, err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return zero, err
		}
		select {
		case <-ctx.Done():
			return zero, err
		case <-time.After(min(poll, remaining)):
		}
	}
}
