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

// The watch manager tests drive Manager's real per-watch delivery goroutines,
// which is what makes them integration tests rather than pure unit tests. They
// live in manager_integration_test.go behind the integration build tag.
//
// They no longer synchronise on time.Sleep: each waits on the mock stream's
// sent signal via waitResponses, and each assertion that something was NOT
// delivered is ordered behind a later event that WAS (#309).
//
// To run these tests:
//   go test -tags=integration ./pkg/watch/...
