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

package publicverbstest

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// recordingTB is a testing.TB whose Fatalf records the failure instead of
// stopping the goroutine, so a test can assert that CheckHealth fails.
type recordingTB struct {
	testing.TB
	failed bool
	msg    string
}

func (r *recordingTB) Fatalf(format string, args ...any) {
	r.failed = true
	r.msg = fmt.Sprintf(format, args...)
}

func TestWantHealthEveryFieldSet(t *testing.T) {
	if WantHealth.Status == "" || !WantHealth.Leader || WantHealth.DiagnosticsDropped == 0 {
		t.Fatalf("WantHealth = %+v, want every field non-zero", WantHealth)
	}
}

func TestAdminHealthReturnsCopyOfWantHealth(t *testing.T) {
	a := Admin{}
	got, err := a.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if *got != WantHealth {
		t.Fatalf("Health = %+v, want %+v", *got, WantHealth)
	}
	got.Status = "changed"
	if WantHealth.Status != "ok" {
		t.Fatalf("Health returned WantHealth itself; mutating it changed the fixture to %+v", WantHealth)
	}
}

func TestCheckHealthPassesOnWantHealth(t *testing.T) {
	r := &recordingTB{TB: t}
	CheckHealth(r, "grpc", WantHealth)
	if r.failed {
		t.Fatalf("CheckHealth failed on WantHealth: %s", r.msg)
	}
}

func TestCheckHealthFailsOnEachDifferentField(t *testing.T) {
	cases := map[string]service.HealthStatus{
		"status":  {Status: "degraded", Leader: WantHealth.Leader, DiagnosticsDropped: WantHealth.DiagnosticsDropped},
		"leader":  {Status: WantHealth.Status, Leader: false, DiagnosticsDropped: WantHealth.DiagnosticsDropped},
		"dropped": {Status: WantHealth.Status, Leader: WantHealth.Leader, DiagnosticsDropped: 0},
		"zero":    {},
	}
	for name, got := range cases {
		t.Run(name, func(t *testing.T) {
			r := &recordingTB{TB: t}
			CheckHealth(r, "quic", got)
			if !r.failed {
				t.Fatalf("CheckHealth passed on %+v", got)
			}
			if !strings.HasPrefix(r.msg, "quic Health with no credential") {
				t.Fatalf("failure message %q does not name the transport", r.msg)
			}
		})
	}
}
