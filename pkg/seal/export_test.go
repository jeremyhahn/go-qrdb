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

// Package seal — export_test.go exposes internal test helpers to the
// external test package (package seal_test) via exported type aliases and
// constructor vars.

package seal

// CaptureDistributor is a type alias for captureDistributor, exporting it to
// external (package seal_test) test files. It implements CeremonyDistributor,
// splits shares with the real Shamir library, and captures them for
// round-trip testing of UnsealWithShare / RecoverWithKeys.
type CaptureDistributor = captureDistributor

// NewCaptureDistributor creates a CaptureDistributor for use in tests.
var NewCaptureDistributor = newCaptureDistributor
