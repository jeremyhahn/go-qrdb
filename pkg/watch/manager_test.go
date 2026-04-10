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

package watch

// All watch manager tests use time.Sleep for goroutine synchronization and
// real goroutine spawning, making them integration tests rather than pure unit tests.
//
// These tests have been moved to manager_integration_test.go with proper build tags.
//
// To run these tests:
//   go test -tags=integration ./pkg/watch/...
