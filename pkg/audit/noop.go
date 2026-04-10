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

// noopLogger silently discards all audit entries. It is returned when
// auditing is disabled or when no backends are configured.
type noopLogger struct{}

// Log discards the entry and returns nil.
func (n *noopLogger) Log(_ context.Context, _ *Entry) error { return nil }

// Close is a no-op and returns nil.
func (n *noopLogger) Close() error { return nil }
