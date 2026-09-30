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

package secure_test

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	securesdk "github.com/jeremyhahn/go-qrdb/sdk/go/secure"
)

// HardenProcess reaches a consumer through the SDK with its opt-out and its
// typed refusal intact. Neither test lets the platform mechanism run: applying
// it would make this test binary non-dumpable for the rest of its life. The
// mechanism itself is proven from a subprocess in pkg/secure.

func TestHardenProcess_OptOutIsHonouredThroughTheSDK(t *testing.T) {
	t.Setenv(securesdk.AllowProcessInspectionEnv, "true")
	var out bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&out, nil))
	if err := securesdk.HardenProcess(logger); err != nil {
		t.Fatalf("HardenProcess: %v", err)
	}
	if !strings.Contains(out.String(), "level=WARN") ||
		!strings.Contains(out.String(), securesdk.AllowProcessInspectionEnv) {
		t.Fatalf("the opt-out must warn, naming the variable; got %q", out.String())
	}
}

func TestHardenProcess_MalformedOptOutIsATypedRefusalThroughTheSDK(t *testing.T) {
	t.Setenv(securesdk.AllowProcessInspectionEnv, "maybe")
	err := securesdk.HardenProcess(nil)
	var optErr *securesdk.ProcessInspectionOptOutError
	if !errors.As(err, &optErr) {
		t.Fatalf("HardenProcess = %v (%T), want *ProcessInspectionOptOutError", err, err)
	}
	var hardErr *securesdk.ProcessHardeningError
	if errors.As(err, &hardErr) {
		t.Fatalf("a malformed opt-out is not a platform refusal; got %v", err)
	}
}
