// Copyright 2026 Jeremy Hahn
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

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func newBatchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "batch",
		Short: "Batch operations",
	}
	cmd.AddCommand(newBatchExecuteCmd())
	cmd.AddCommand(newBatchImportCmd())
	cmd.AddCommand(newBatchImportFormatsCmd())
	return cmd
}

func newBatchExecuteCmd() *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "execute",
		Short: "Execute a batch of operations from a JSON file",
		RunE: func(cmd *cobra.Command, args []string) error {
			var data []byte
			var err error
			if file == "-" {
				data, err = io.ReadAll(os.Stdin)
				if err != nil {
					return &FileReadError{Path: "stdin", Err: err}
				}
			} else {
				data, err = os.ReadFile(file)
				if err != nil {
					return &FileReadError{Path: file, Err: err}
				}
			}

			var req transport.BatchRequest
			if err := json.Unmarshal(data, &req); err != nil {
				return err
			}

			ctx := cmd.Context()
			resp, err := client.Batch().ExecuteBatch(ctx, req.Ops)
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				enc := json.NewEncoder(outputWriter)
				enc.SetIndent("", "  ")
				return enc.Encode(resp)
			case "table":
				for i, r := range resp.Responses {
					_, _ = fmt.Fprintf(outputWriter, "Response[%s]: succeeded=%t count=%d\n",
						strconv.Itoa(i), r.Succeeded, r.Count)
				}
				return nil
			default:
				enc := json.NewEncoder(outputWriter)
				enc.SetIndent("", "  ")
				return enc.Encode(resp)
			}
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "JSON file path (use \"-\" for stdin)")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}

// availableImportFormats is the supported --format values for
// 'batch import'. It mirrors the GUI's BatchBinding.AvailableImportFormats
// (bindings_batch.go), which is likewise a static client-side list rather
// than something fetched from the server.
var availableImportFormats = []string{"auto", "csv", "json", "ndjson", "yaml", "archive"}

// newBatchImportCmd returns the "batch import" subcommand, which parses
// and imports key-value records from a file via client.Batch().ImportFile
// -- the same execution core the desktop GUI's BatchView import panel
// uses.
func newBatchImportCmd() *cobra.Command {
	var (
		file          string
		format        string
		keyField      string
		valueMode     string
		valueField    string
		skipHeader    bool
		transactional bool
	)

	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import key-value records from a file",
		Long: `Import key-value records from a CSV, JSON, NDJSON, YAML, or archive file
into the KV store.

--format selects the parser; "auto" (the default) infers the format from
the file extension and the first 8 KB of content -- see 'batch
import-formats' for the supported values. --key-field names the field to
extract as the record key. --value-mode selects how the value is derived:
"field" uses --value-field's value, "row" serializes the entire row as
JSON (the default).

Imports always land in the raw KV keyspace; there is no collection-targeted
import (collections only support inserting one document at a time via
'qrdb collection doc insert'). Use the global --database flag (see
'qrdb --help') to scope the import to a named database -- /batch is a
data-plane endpoint, so the globally-scoped connection already carries
that database context on every request.

There is no maximum import file size: the file is carried as a chunked
import session (ImportBegin/ImportAppend/ImportCommit) transparently, in
pieces no larger than the active transport's own small pre-auth message
cap, identical on every transport.

Examples:
  qrdb batch import --file data.csv --key-field id --value-mode row
  qrdb batch import --file - --format ndjson --key-field id --value-mode field --value-field payload`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if file == "" {
				return &MissingArgError{Arg: "--file"}
			}

			var (
				data     io.Reader
				filename string
			)
			if file == "-" {
				data = os.Stdin
			} else {
				f, err := os.Open(file)
				if err != nil {
					return &FileOpenError{Path: file, Err: err}
				}
				defer func() { _ = f.Close() }()
				data = f
				filename = filepath.Base(file)
			}

			opts := transport.ImportOptions{
				Format:        format,
				Filename:      filename,
				KeyField:      keyField,
				ValueMode:     valueMode,
				ValueField:    valueField,
				SkipHeader:    skipHeader,
				Transactional: transactional,
			}

			result, err := client.Batch().ImportFile(cmd.Context(), filename, data, opts)
			if err != nil {
				return err
			}

			return renderImportResult(result)
		},
	}

	cmd.Flags().StringVar(&file, "file", "", "Path to the file to import (\"-\" for stdin)")
	_ = cmd.MarkFlagRequired("file")
	cmd.Flags().StringVar(&format, "format", "auto", "Import format: auto | csv | json | ndjson | yaml | archive")
	cmd.Flags().StringVar(&keyField, "key-field", "", "Field name to extract as the record key")
	cmd.Flags().StringVar(&valueMode, "value-mode", "row", "Value extraction mode: field | row")
	cmd.Flags().StringVar(&valueField, "value-field", "", "Field name for the value when --value-mode=field")
	cmd.Flags().BoolVar(&skipHeader, "skip-header", false, "Skip the first row (CSV only)")
	cmd.Flags().BoolVar(&transactional, "transactional", false, "All-or-nothing import: any row error rolls back the entire import")

	return cmd
}

// renderImportResult prints an ImportResult in the configured output
// format.
func renderImportResult(result *transport.ImportResult) error {
	switch outputFormat {
	case "json":
		return printJSON(result)
	case "yaml":
		return printYAML(result)
	default:
		_, _ = fmt.Fprintf(outputWriter, "Total:     %d\n", result.Total)
		_, _ = fmt.Fprintf(outputWriter, "Succeeded: %d\n", result.Succeeded)
		_, _ = fmt.Fprintf(outputWriter, "Failed:    %d\n", result.Failed)
		if len(result.Errors) > 0 {
			_, _ = fmt.Fprintln(outputWriter, "Errors:")
			for _, e := range result.Errors {
				_, _ = fmt.Fprintf(outputWriter, "  row=%d key=%s: %s\n", e.Row, e.Key, e.Message)
			}
		}
		return nil
	}
}

// newBatchImportFormatsCmd returns the "batch import-formats" subcommand,
// which lists the file formats 'batch import --format' accepts.
func newBatchImportFormatsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "import-formats",
		Short: "List supported batch import file formats",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return printList(availableImportFormats, outputFormat)
		},
	}
}
