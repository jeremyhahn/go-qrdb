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

package wiring

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// The mTLS binding posture as it is WIRED (go-qrdb#517): the middleware
// enforces it, and these prove it survives BuildAuthMiddleware, the one
// constructor qrdbd and the SDK node share.

func TestBuildAuthMiddleware_MTLSBindingPolicy(t *testing.T) {
	for name, tc := range map[string]struct {
		set  auth.MTLSBindingPolicy
		want auth.MTLSBindingPolicy
	}{
		"unset is required": {want: auth.MTLSBindingRequired},
		"enrolled reaches":  {set: auth.MTLSBindingEnrolled, want: auth.MTLSBindingEnrolled},
	} {
		t.Run(name, func(t *testing.T) {
			mw, err := BuildAuthMiddleware(AuthMiddlewareConfig{
				Enabled:           true,
				RoleService:       adminRoleService(),
				UserService:       &fakeUserService{},
				MTLSBindingPolicy: tc.set,
			})
			require.NoError(t, err)
			assert.Equal(t, tc.want, mw.MTLSBindingPolicy())
		})
	}
}
