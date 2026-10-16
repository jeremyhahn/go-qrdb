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

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"gopkg.in/yaml.v3"

	"github.com/jeremyhahn/go-qrdb/sdk/go/secure"
)

// outputWriter is the destination for all CLI output. It defaults to
// os.Stdout but can be overridden in tests to capture output.
var outputWriter io.Writer = os.Stdout

// printJSON marshals v to indented JSON and writes it to outputWriter.
func printJSON(v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(outputWriter, string(data))
	return nil
}

// printYAML marshals v to YAML and writes it to outputWriter.
func printYAML(v any) error {
	data, err := yaml.Marshal(v)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprint(outputWriter, string(data))
	return nil
}

// printTable writes a simple aligned table to outputWriter using tabwriter.
func printTable(headers []string, rows [][]string) {
	w := tabwriter.NewWriter(outputWriter, 0, 0, 2, ' ', 0)
	for i, h := range headers {
		if i > 0 {
			_, _ = fmt.Fprint(w, "\t")
		}
		_, _ = fmt.Fprint(w, h)
	}
	_, _ = fmt.Fprintln(w)
	for _, row := range rows {
		for i, cell := range row {
			if i > 0 {
				_, _ = fmt.Fprint(w, "\t")
			}
			_, _ = fmt.Fprint(w, cell)
		}
		_, _ = fmt.Fprintln(w)
	}
	_ = w.Flush()
}

// printValue writes a single value to outputWriter.
func printValue(v string) {
	_, _ = fmt.Fprintln(outputWriter, v)
}

// printSecretBuffer writes the value held in buf, followed by a newline, to
// outputWriter and destroys buf on every path. The CLI is the output boundary
// for a retrieved credential: the value goes from protected memory straight to
// the writer without being converted to a string, which Go cannot wipe.
func printSecretBuffer(buf *secure.Buffer) error {
	defer buf.Destroy()
	return buf.WithBytes(func(b []byte) error {
		if _, err := outputWriter.Write(b); err != nil {
			return err
		}
		_, err := io.WriteString(outputWriter, "\n")
		return err
	})
}

// printOK writes "OK" to outputWriter for void operations.
func printOK() {
	_, _ = fmt.Fprintln(outputWriter, "OK")
}

// printList writes items in the configured output format. In table mode,
// items are printed one per line. In json mode, they are printed as a
// JSON array. In yaml mode, they are printed as a YAML sequence.
func printList(items []string, format string) error {
	switch format {
	case "json":
		return printJSON(items)
	case "yaml":
		return printYAML(items)
	default:
		for _, item := range items {
			_, _ = fmt.Fprintln(outputWriter, item)
		}
		return nil
	}
}
