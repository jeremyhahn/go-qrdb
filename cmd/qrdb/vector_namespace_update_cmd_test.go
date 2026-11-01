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

//go:build qrdb_vector && !integration

package main

import (
	"errors"
	"strings"
	"testing"
)

func TestVectorCmd_NamespaceUpdate_Success(t *testing.T) {
	out, err := executeVectorCmd(t, "namespace", "update",
		"--namespace", "myns",
		"--index-param", "ef_search=128",
		"--metadata", `{"owner":"search"}`,
	)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "Requires rebuild:") {
		t.Fatalf("output = %q, want the rebuild flag", out)
	}
}

func TestVectorCmd_NamespaceUpdate_OutputFormats(t *testing.T) {
	for _, format := range []string{"json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			if _, err := executeVectorCmdWithFormat(t, format, "namespace", "update", "--namespace", "myns"); err != nil {
				t.Fatalf("expected no error, got: %v", err)
			}
		})
	}
}

func TestVectorCmd_NamespaceUpdate_MissingNamespace(t *testing.T) {
	if _, err := executeVectorCmd(t, "namespace", "update", "--index-param", "ef_search=128"); err == nil {
		t.Fatal("expected error for missing --namespace flag")
	}
}

func TestVectorCmd_NamespaceUpdate_InvalidMetadata(t *testing.T) {
	_, err := executeVectorCmd(t, "namespace", "update", "--namespace", "myns", "--metadata", "not-json")
	if _, ok := errors.AsType[*JSONParseError](err); !ok {
		t.Fatalf("err = %v (%T), want *JSONParseError", err, err)
	}
}
