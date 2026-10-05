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

// This file exists ONLY to let an EXTERNAL test package (package
// firstboot_test) reach otherwise-unexported internals -- the standard
// library's own "export_test.go" pattern. It adds no production surface:
// the _test.go suffix excludes it from every non-test build.
//
// role_race_cluster_test.go needs to be an EXTERNAL test package because
// it imports pkg/service (to build a real, guarded KV store the way
// cmd/qrdbd itself does), and pkg/service imports BACK into this package
// (ceremony_admin_rebind.go, ceremony_leader_gate.go) -- an internal test
// file importing pkg/service would close that into an import cycle the
// production code never has.

// RegisterBuiltInRoles is registerBuiltInRoles, exported for
// role_race_cluster_test.go.
var RegisterBuiltInRoles = registerBuiltInRoles

// RoleMatchesSeed is roleMatchesSeed, exported for
// role_race_cluster_test.go.
var RoleMatchesSeed = roleMatchesSeed
