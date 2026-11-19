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

package sdk

import (
	"crypto/tls"
	"testing"
	"time"
)

func TestWithTLSConfig_SetsTransportOption(t *testing.T) {
	opts := &clientOptions{}
	WithTLSConfig(&tls.Config{MinVersion: tls.VersionTLS13})(opts)
	if len(opts.transportOpts) != 1 {
		t.Errorf("expected 1 transport option, got %d", len(opts.transportOpts))
	}
}

func TestClientOptions_Accumulate(t *testing.T) {
	opts := &clientOptions{}
	for _, opt := range []ClientOption{
		WithAddress("https://localhost:8080"),
		WithTimeout(10 * time.Second),
		WithTLS("/c.pem", "/k.pem", "/ca.pem"),
	} {
		opt(opts)
	}
	if len(opts.transportOpts) != 3 {
		t.Errorf("expected 3 transport options, got %d", len(opts.transportOpts))
	}
}
