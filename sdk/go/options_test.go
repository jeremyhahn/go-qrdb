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

func TestWithAddress_SetsTransportOption(t *testing.T) {
	opts := applyClientOptions([]ClientOption{
		WithAddress("https://localhost:8080"),
	})
	if len(opts.transportOpts) != 1 {
		t.Errorf("expected 1 transport option, got %d", len(opts.transportOpts))
	}
}

func TestWithTLS_SetsTransportOption(t *testing.T) {
	opts := applyClientOptions([]ClientOption{
		WithTLS("/cert.pem", "/key.pem", "/ca.pem"),
	})
	if len(opts.transportOpts) != 1 {
		t.Errorf("expected 1 transport option, got %d", len(opts.transportOpts))
	}
}

func TestWithTLSConfig_SetsTransportOption(t *testing.T) {
	opts := applyClientOptions([]ClientOption{
		WithTLSConfig(&tls.Config{MinVersion: tls.VersionTLS13}),
	})
	if len(opts.transportOpts) != 1 {
		t.Errorf("expected 1 transport option, got %d", len(opts.transportOpts))
	}
}

func TestWithTimeout_SetsTransportOption(t *testing.T) {
	opts := applyClientOptions([]ClientOption{
		WithTimeout(5 * time.Second),
	})
	if len(opts.transportOpts) != 1 {
		t.Errorf("expected 1 transport option, got %d", len(opts.transportOpts))
	}
}

func TestApplyClientOptions_Empty(t *testing.T) {
	opts := applyClientOptions(nil)
	if len(opts.transportOpts) != 0 {
		t.Errorf("expected 0 transport options, got %d", len(opts.transportOpts))
	}
}

func TestApplyClientOptions_Multiple(t *testing.T) {
	opts := applyClientOptions([]ClientOption{
		WithAddress("https://localhost:8080"),
		WithTimeout(10 * time.Second),
		WithTLS("/c.pem", "/k.pem", "/ca.pem"),
	})
	if len(opts.transportOpts) != 3 {
		t.Errorf("expected 3 transport options, got %d", len(opts.transportOpts))
	}
}
