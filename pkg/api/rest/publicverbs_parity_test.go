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

package rest

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/publicverbs"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
)

// TestPublicVerbsParity_REST fails when a publicverbs.Table row is not a
// public REST route. The other direction -- no route outside the table and
// the node-plane exemptions is public -- is
// TestRouteMappingGuard_ExemptRoutesAreExactlyDeclared, which takes its
// client verbs from the same table.
func TestPublicVerbsParity_REST(t *testing.T) {
	srv := newFullyWiredGuardServer(t)
	for _, v := range publicverbs.Verbs {
		t.Run(string(v), func(t *testing.T) {
			method, path := publicverbs.RESTRoute(v)
			if _, kind, ok := srv.routePerms.Lookup(method, path); !ok || kind != authmw.RESTRoutePublic {
				t.Errorf("%s %s (%s): ok=%v kind=%v, want RESTRoutePublic", method, path, v, ok, kind)
			}
		})
	}
}
