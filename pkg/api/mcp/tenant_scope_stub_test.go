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

package mcp

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// allowAllTenantScope is a tenantScopeAuthorizer stub that permits every
// target tenant, isolating handler-behavior tests from authorization. It
// carries no build constraint, so every test compilation context that
// includes the tenant-barrier or enable-encryption handlers can reference it.
func allowAllTenantScope(_ context.Context, _ string, _ permission.Permission) error {
	return nil
}
