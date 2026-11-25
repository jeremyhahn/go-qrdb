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

package firstboot

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// ClearPendingSystemSOInBackground runs ClearPendingSystemSOIfEnrolled off
// the caller's goroutine and returns at once; the returned channel closes
// when the background work ends.
//
// The SO PIN check is a linearizable read: on a clustered node that boots
// before its peers it blocks until the cluster has a quorum, which it cannot
// form while the node is still booting and not yet serving. Boot therefore
// never waits on it. The check is retried every interval, up to attempts
// times, while the SO is not enrolled yet or a check fails; a record whose
// enrollment is refuted (*SystemSOUnverifiedError) stops the retries. A
// record still pending at the end is kept for the next start, exactly as a
// synchronous check that found the SO not enrolled would keep it.
func ClearPendingSystemSOInBackground(ctx context.Context, store PendingAdminCertStore, users UserResolver, pins SOPINChecker, interval time.Duration, attempts int, logger *slog.Logger) <-chan struct{} {
	if logger == nil {
		logger = slog.Default()
	}
	if attempts < 1 {
		attempts = 1
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for attempt := 1; ; attempt++ {
			cleared, err := ClearPendingSystemSOIfEnrolled(ctx, store, users, pins)
			if err == nil && cleared {
				return
			}
			var unverified *SystemSOUnverifiedError
			if errors.As(err, &unverified) {
				logger.Warn("first-boot: the pending system SO is not verified; keeping its record", slog.Any("error", err))
				return
			}
			if err != nil {
				logger.Warn("first-boot: failed to clear the pending system SO record", slog.Any("error", err), slog.Int("attempt", attempt))
			}
			if attempt >= attempts {
				logger.Warn("first-boot: the pending system SO is not enrolled yet; keeping its record for the next start")
				return
			}
			timer := time.NewTimer(interval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
	return done
}
