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
	"context"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// nodeCompactionBody is the request and response body of
// /admin/node-compaction. An absent compaction is no policy.
type nodeCompactionBody struct {
	Compaction *transport.DatabaseCompactionConfig `json:"compaction,omitempty"`
}

// NodeCompaction returns the platform's node-keyspace compaction policy.
// Maps to GET /admin/node-compaction.
func (t *Transport) NodeCompaction(ctx context.Context) (*transport.DatabaseCompactionConfig, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	var out nodeCompactionBody
	if err := t.getJSONDecode(ctx, "NodeCompaction", "/admin/node-compaction", &out); err != nil {
		return nil, err
	}
	return out.Compaction, nil
}

// SetNodeCompaction sets the platform's node-keyspace compaction policy; nil
// turns it off. Maps to PUT /admin/node-compaction.
func (t *Transport) SetNodeCompaction(ctx context.Context, policy *transport.DatabaseCompactionConfig) (*transport.DatabaseCompactionConfig, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	var out nodeCompactionBody
	if err := t.putJSONDecode(ctx, "SetNodeCompaction", "/admin/node-compaction", nodeCompactionBody{Compaction: policy}, &out); err != nil {
		return nil, err
	}
	return out.Compaction, nil
}
