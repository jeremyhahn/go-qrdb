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
	"strconv"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// effectiveDatabaseScope resolves the database a command with its own local
// --database flag (collection.go, sql.go) should target.
//
// The persistent root --database flag (root.go) scopes ONLY the data-plane
// KV/batch/tx/watch/lease/lock/timeseries/index/vector commands, by wrapping
// the SDK client itself (scopeClientToDatabase); it never reaches a command
// whose request carries its own explicit Database field. Collection and SQL
// commands are exactly that case: they declare their own local --database
// flag (default transport.DefaultDatabaseScope), which is a second,
// independently-scoped flag that happens to share the same name. Without
// this reconciliation, `qrdb --database mydb collection list` silently
// lists "system" — the global flag is accepted but has no effect, a
// foot-gun with no error or warning.
//
// The rule: an explicit local --database always wins (it is the more
// specific request); otherwise, when the root --database flag was set, it
// is honored instead of silently falling back to
// transport.DefaultDatabaseScope. cmd must be the leaf subcommand that
// registered the local "database" flag (e.g. the "collection list" or "sql
// query" command instance passed into its own RunE), so cmd.Flags() sees
// that flag's Changed state and cmd.Root() reaches the root command's
// PersistentFlags regardless of how deep the command tree is.
func effectiveDatabaseScope(cmd *cobra.Command, local string) string {
	if cmd.Flags().Changed("database") {
		return local
	}
	root := cmd.Root().PersistentFlags()
	if root == nil {
		return local
	}
	global, err := root.GetString("database")
	if err != nil || global == "" {
		return local
	}
	return global
}

// newCollectionCmd creates the "collection" command group with
// subcommands for document-collection management (create, info, list,
// drop) and, nested under "doc", per-document CRUD operations.
func newCollectionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "collection",
		Short: "Document collection management",
		Long:  "Create, inspect, list, and drop document collections, and manage the documents within them.",
	}

	cmd.AddCommand(
		newCollectionCreateCmd(),
		newCollectionInfoCmd(),
		newCollectionListCmd(),
		newCollectionDropCmd(),
		newCollectionDocCmd(),
	)

	return cmd
}

// newCollectionCreateCmd creates the "collection create" subcommand.
func newCollectionCreateCmd() *cobra.Command {
	var (
		database     string
		idStrategy   string
		fieldHash    string
		storageCodec string
	)

	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new collection within a database",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := transport.CreateCollectionRequest{
				Database:     effectiveDatabaseScope(cmd, database),
				Name:         args[0],
				IDStrategy:   idStrategy,
				FieldHash:    fieldHash,
				StorageCodec: storageCodec,
			}

			info, err := client.Collections().CreateCollection(cmd.Context(), req)
			if err != nil {
				return err
			}

			return printCollectionInfo(info)
		},
	}

	cmd.Flags().StringVar(&database, "database", transport.DefaultDatabaseScope, "Target database")
	cmd.Flags().StringVar(&idStrategy, "id-strategy", "", "Document ID strategy: unix_nano, increment, or field_hash (default: unix_nano)")
	cmd.Flags().StringVar(&fieldHash, "field-hash", "", "Document field to hash (required when --id-strategy=field_hash)")
	cmd.Flags().StringVar(&storageCodec, "storage-codec", "", "Document storage codec override")

	return cmd
}

// newCollectionInfoCmd creates the "collection info" subcommand.
func newCollectionInfoCmd() *cobra.Command {
	var database string

	cmd := &cobra.Command{
		Use:   "info <name>",
		Short: "Show collection details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			info, err := client.Collections().GetCollectionInfo(cmd.Context(), effectiveDatabaseScope(cmd, database), args[0])
			if err != nil {
				return err
			}

			return printCollectionInfo(info)
		},
	}

	cmd.Flags().StringVar(&database, "database", transport.DefaultDatabaseScope, "Target database")

	return cmd
}

// newCollectionListCmd creates the "collection list" subcommand.
func newCollectionListCmd() *cobra.Command {
	var database string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all collections in a database",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := client.Collections().ListCollections(cmd.Context(), effectiveDatabaseScope(cmd, database))
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			default:
				headers := []string{"NAME", "DATABASE", "ID_STRATEGY", "DOCUMENTS", "STORAGE_CODEC", "CREATED_AT"}
				rows := make([][]string, len(resp.Collections))
				for i := range resp.Collections {
					rows[i] = collectionRow(&resp.Collections[i])
				}
				printTable(headers, rows)
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&database, "database", transport.DefaultDatabaseScope, "Target database")

	return cmd
}

// newCollectionDropCmd creates the "collection drop" subcommand.
func newCollectionDropCmd() *cobra.Command {
	var database string

	cmd := &cobra.Command{
		Use:   "drop <name>",
		Short: "Drop a collection",
		Long:  "Drop a collection and delete all of its documents. This operation is irreversible.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := client.Collections().DropCollection(cmd.Context(), effectiveDatabaseScope(cmd, database), args[0]); err != nil {
				return err
			}

			printOK()
			return nil
		},
	}

	cmd.Flags().StringVar(&database, "database", transport.DefaultDatabaseScope, "Target database")

	return cmd
}

// newCollectionDocCmd creates the "collection doc" command group with
// subcommands for per-document CRUD operations.
func newCollectionDocCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doc",
		Short: "Document operations within a collection",
		Long:  "Insert, retrieve, update, delete, and list documents within a collection.",
	}

	cmd.AddCommand(
		newCollectionDocInsertCmd(),
		newCollectionDocGetCmd(),
		newCollectionDocUpdateCmd(),
		newCollectionDocDeleteCmd(),
		newCollectionDocListCmd(),
	)

	return cmd
}

// newCollectionDocInsertCmd creates the "collection doc insert" subcommand.
func newCollectionDocInsertCmd() *cobra.Command {
	var (
		database string
		data     string
		file     string
	)

	cmd := &cobra.Command{
		Use:   "insert <collection>",
		Short: "Insert a document into a collection",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			doc, err := readDocumentJSON(data, file)
			if err != nil {
				return err
			}

			req := transport.InsertDocumentRequest{
				Database:   effectiveDatabaseScope(cmd, database),
				Collection: args[0],
				Document:   doc,
			}

			resp, err := client.Collections().InsertDocument(cmd.Context(), req)
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			default:
				_, _ = fmt.Fprintf(outputWriter, "ID: %s\n", strconv.FormatUint(resp.ID, 10))
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&database, "database", transport.DefaultDatabaseScope, "Target database")
	cmd.Flags().StringVar(&data, "data", "", "Document JSON (inline)")
	cmd.Flags().StringVar(&file, "file", "", "Document JSON file path (use \"-\" for stdin)")
	cmd.MarkFlagsMutuallyExclusive("data", "file")

	return cmd
}

// newCollectionDocGetCmd creates the "collection doc get" subcommand.
func newCollectionDocGetCmd() *cobra.Command {
	var database string

	cmd := &cobra.Command{
		Use:   "get <collection> <id>",
		Short: "Retrieve a document by ID",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseDocumentID(args[1])
			if err != nil {
				return err
			}

			doc, err := client.Collections().GetDocument(cmd.Context(), effectiveDatabaseScope(cmd, database), args[0], id)
			if err != nil {
				return err
			}

			return printDocumentResponse(doc)
		},
	}

	cmd.Flags().StringVar(&database, "database", transport.DefaultDatabaseScope, "Target database")

	return cmd
}

// newCollectionDocUpdateCmd creates the "collection doc update" subcommand.
func newCollectionDocUpdateCmd() *cobra.Command {
	var (
		database string
		data     string
		file     string
	)

	cmd := &cobra.Command{
		Use:   "update <collection> <id>",
		Short: "Replace an existing document",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseDocumentID(args[1])
			if err != nil {
				return err
			}

			doc, err := readDocumentJSON(data, file)
			if err != nil {
				return err
			}

			req := transport.UpdateDocumentRequest{
				Database:   effectiveDatabaseScope(cmd, database),
				Collection: args[0],
				ID:         id,
				Document:   doc,
			}

			if err := client.Collections().UpdateDocument(cmd.Context(), req); err != nil {
				return err
			}

			printOK()
			return nil
		},
	}

	cmd.Flags().StringVar(&database, "database", transport.DefaultDatabaseScope, "Target database")
	cmd.Flags().StringVar(&data, "data", "", "Document JSON (inline)")
	cmd.Flags().StringVar(&file, "file", "", "Document JSON file path (use \"-\" for stdin)")
	cmd.MarkFlagsMutuallyExclusive("data", "file")

	return cmd
}

// newCollectionDocDeleteCmd creates the "collection doc delete" subcommand.
func newCollectionDocDeleteCmd() *cobra.Command {
	var database string

	cmd := &cobra.Command{
		Use:   "delete <collection> <id>",
		Short: "Delete a document by ID",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseDocumentID(args[1])
			if err != nil {
				return err
			}

			if err := client.Collections().DeleteDocument(cmd.Context(), effectiveDatabaseScope(cmd, database), args[0], id); err != nil {
				return err
			}

			printOK()
			return nil
		},
	}

	cmd.Flags().StringVar(&database, "database", transport.DefaultDatabaseScope, "Target database")

	return cmd
}

// newCollectionDocListCmd creates the "collection doc list" subcommand.
func newCollectionDocListCmd() *cobra.Command {
	var (
		database  string
		pageSize  int32
		pageToken string
	)

	cmd := &cobra.Command{
		Use:   "list <collection>",
		Short: "List documents in a collection",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := transport.ListDocumentsRequest{
				Database:   effectiveDatabaseScope(cmd, database),
				Collection: args[0],
				PageSize:   pageSize,
				PageToken:  pageToken,
			}

			resp, err := client.Collections().ListDocuments(cmd.Context(), req)
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			default:
				headers := []string{"ID", "DOCUMENT"}
				rows := make([][]string, len(resp.Documents))
				for i := range resp.Documents {
					rows[i] = documentRow(&resp.Documents[i])
				}
				printTable(headers, rows)
				if resp.NextPageToken != "" {
					_, _ = fmt.Fprintf(outputWriter, "\nNextPageToken: %s (Total: %s)\n",
						resp.NextPageToken, strconv.FormatInt(resp.TotalCount, 10))
				}
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&database, "database", transport.DefaultDatabaseScope, "Target database")
	cmd.Flags().Int32Var(&pageSize, "page-size", 0, "Number of results per page")
	cmd.Flags().StringVar(&pageToken, "page-token", "", "Pagination token from a previous response")

	return cmd
}

// printCollectionInfo prints a single CollectionInfo in the current
// output format.
func printCollectionInfo(info *transport.CollectionInfo) error {
	switch outputFormat {
	case "json":
		return printJSON(info)
	case "yaml":
		return printYAML(info)
	default:
		_, _ = fmt.Fprintf(outputWriter, "Name:          %s\n", info.Name)
		_, _ = fmt.Fprintf(outputWriter, "Database:      %s\n", info.Database)
		_, _ = fmt.Fprintf(outputWriter, "IDStrategy:    %s\n", info.IDStrategy)
		if info.FieldHash != "" {
			_, _ = fmt.Fprintf(outputWriter, "FieldHash:     %s\n", info.FieldHash)
		}
		if info.StorageCodec != "" {
			_, _ = fmt.Fprintf(outputWriter, "StorageCodec:  %s\n", info.StorageCodec)
		}
		_, _ = fmt.Fprintf(outputWriter, "DocumentCount: %s\n", strconv.FormatInt(info.DocumentCount, 10))
		_, _ = fmt.Fprintf(outputWriter, "CreatedAt:     %s\n", info.CreatedAt)
		if len(info.Schema) > 0 {
			_, _ = fmt.Fprintf(outputWriter, "Schema:\n")
			for field, kind := range info.Schema {
				_, _ = fmt.Fprintf(outputWriter, "  %s: %s\n", field, kind)
			}
		}
		return nil
	}
}

// collectionRow converts a CollectionInfo into a table row.
func collectionRow(info *transport.CollectionInfo) []string {
	return []string{
		info.Name,
		info.Database,
		info.IDStrategy,
		strconv.FormatInt(info.DocumentCount, 10),
		info.StorageCodec,
		info.CreatedAt,
	}
}

// printDocumentResponse prints a single DocumentResponse in the current
// output format.
func printDocumentResponse(doc *transport.DocumentResponse) error {
	switch outputFormat {
	case "json":
		return printJSON(doc)
	case "yaml":
		return printYAML(doc)
	default:
		body, err := json.Marshal(doc.Document)
		if err != nil {
			return &DocumentJSONError{Err: err}
		}
		_, _ = fmt.Fprintf(outputWriter, "ID:       %s\n", strconv.FormatUint(doc.ID, 10))
		_, _ = fmt.Fprintf(outputWriter, "Document: %s\n", string(body))
		return nil
	}
}

// documentRow converts a DocumentResponse into a table row, rendering
// the document body as compact JSON.
func documentRow(doc *transport.DocumentResponse) []string {
	body, err := json.Marshal(doc.Document)
	if err != nil {
		body = []byte("<invalid document>")
	}
	return []string{
		strconv.FormatUint(doc.ID, 10),
		string(body),
	}
}

// parseDocumentID parses a document ID argument as a uint64.
func parseDocumentID(s string) (uint64, error) {
	id, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, &InvalidDocumentIDError{Input: s}
	}
	return id, nil
}

// readDocumentJSON resolves a document body from either an inline --data
// string or a --file path ("-" reads from stdin), and parses it as a
// JSON object. Exactly one of data or file must be non-empty.
func readDocumentJSON(data, file string) (map[string]interface{}, error) {
	var raw []byte

	switch {
	case data != "":
		raw = []byte(data)
	case file != "":
		var err error
		if file == "-" {
			raw, err = io.ReadAll(os.Stdin)
			if err != nil {
				return nil, &FileReadError{Path: "stdin", Err: err}
			}
		} else {
			raw, err = os.ReadFile(file)
			if err != nil {
				return nil, &FileReadError{Path: file, Err: err}
			}
		}
	default:
		return nil, &MissingArgError{Arg: "--data or --file"}
	}

	var doc map[string]interface{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, &DocumentJSONError{Err: err}
	}
	return doc, nil
}

// InvalidDocumentIDError is returned when a document ID argument cannot
// be parsed as a uint64.
type InvalidDocumentIDError struct {
	Input string
}

// Error returns a human-readable description.
func (e *InvalidDocumentIDError) Error() string {
	return "qrdb: invalid document ID: " + e.Input + ": must be a positive integer"
}

// DocumentJSONError is returned when a document body supplied via --data
// or --file cannot be parsed as (or rendered as) JSON.
type DocumentJSONError struct {
	Err error
}

// Error returns a human-readable description.
func (e *DocumentJSONError) Error() string {
	return "qrdb: invalid document JSON: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *DocumentJSONError) Unwrap() error {
	return e.Err
}
