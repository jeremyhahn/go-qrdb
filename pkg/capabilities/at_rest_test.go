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

package capabilities

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestWarnUnencryptedAtRest_OneWarningPerGap(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	Capabilities{}.WarnUnencryptedAtRest(logger)
	if buf.Len() != 0 {
		t.Fatalf("logged %q for a profile with no gap", buf.String())
	}

	Capabilities{UnencryptedAtRest: []string{"gap one", "gap two"}}.WarnUnencryptedAtRest(logger)
	out := buf.String()
	if strings.Count(out, "level=WARN") != 2 || !strings.Contains(out, "gap one") || !strings.Contains(out, "gap two") {
		t.Fatalf("log = %q, want one WARN per gap", out)
	}
}

func TestWarnUnencryptedAtRest_NilLoggerUsesDefault(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	Capabilities{UnencryptedAtRest: []string{"gap"}}.WarnUnencryptedAtRest(nil)
	if !strings.Contains(buf.String(), "level=WARN") {
		t.Fatalf("default logger got %q, want the warning", buf.String())
	}
}

// The sealed profile's gap list is the profile's own: neither the caller of
// Seal nor a caller of Get can change it afterwards.
func TestSeal_UnencryptedAtRestIsCopied(t *testing.T) {
	Reset()
	t.Cleanup(Reset)

	gaps := []string{"gap"}
	Seal(Capabilities{UnencryptedAtRest: gaps})
	gaps[0] = "changed by the sealer"
	got := Get()
	if got.UnencryptedAtRest[0] != "gap" {
		t.Fatalf("Seal kept the caller's slice: %q", got.UnencryptedAtRest)
	}
	got.UnencryptedAtRest[0] = "changed by a reader"
	if Get().UnencryptedAtRest[0] != "gap" {
		t.Fatal("Get returned the sealed profile's slice")
	}
}
