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

// Package publicverbstest holds the one expected answer every transport's
// no-credential publicverbs.Table verbs must return, so the per-transport
// parity tests compare against the same fixture and a transport that
// answers differently fails.
package publicverbstest

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// WantHealth is the admin service's Health answer the parity servers are
// built with. Every field is non-zero, so a transport that drops or
// rewrites one fails CheckHealth.
var WantHealth = service.HealthStatus{Status: "ok", Leader: true, DiagnosticsDropped: 7}

// Admin wraps a package's admin double so Health returns WantHealth; every
// other method is the wrapped double's.
type Admin struct {
	service.AdminServicer
}

// Health returns a copy of WantHealth.
func (Admin) Health(context.Context) (*service.HealthStatus, error) {
	h := WantHealth
	return &h, nil
}

// CheckHealth fails t when the Health answer a transport decoded differs
// from WantHealth.
func CheckHealth(t testing.TB, transport string, got service.HealthStatus) {
	t.Helper()
	if got != WantHealth {
		t.Fatalf("%s Health with no credential = %+v, want %+v", transport, got, WantHealth)
	}
}
