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

package audit

import "context"

// multiLogger fans out audit entries to multiple backends. It records
// the first error encountered but continues writing to remaining
// backends so that a single backend failure does not suppress the
// audit trail in healthy backends.
type multiLogger struct {
	loggers []Logger
}

// Log writes the entry to every configured backend. The first error
// encountered is returned; subsequent backends are still attempted.
func (m *multiLogger) Log(ctx context.Context, entry *Entry) error {
	var firstErr error
	for _, l := range m.loggers {
		if err := l.Log(ctx, entry); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Close releases resources for every configured backend. The first
// error encountered is returned; subsequent backends are still closed.
func (m *multiLogger) Close() error {
	var firstErr error
	for _, l := range m.loggers {
		if err := l.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
