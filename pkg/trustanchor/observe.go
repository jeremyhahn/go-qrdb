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

package trustanchor

import "context"

// Observe returns r with onChange called after every Add or Remove that
// succeeds. A composer uses it to invalidate a trust pool's cached widening
// the moment a tenant's anchors change on this node, whatever Registry
// implementation it was given. (Other nodes see the change through the
// replicated store when their own cache lapses; the issuer-tenant binding
// reads anchors fresh on every authentication, so a withdrawn anchor stops
// vouching everywhere at once.)
func Observe(r Registry, onChange func()) Registry {
	if r == nil || onChange == nil {
		return r
	}
	return &observed{Registry: r, onChange: onChange}
}

type observed struct {
	Registry
	onChange func()
}

func (o *observed) Add(ctx context.Context, tenantID string, bundlePEM []byte) ([]Anchor, error) {
	out, err := o.Registry.Add(ctx, tenantID, bundlePEM)
	if err == nil {
		o.onChange()
	}
	return out, err
}

func (o *observed) Remove(ctx context.Context, tenantID, fingerprint string) error {
	err := o.Registry.Remove(ctx, tenantID, fingerprint)
	if err == nil {
		o.onChange()
	}
	return err
}
