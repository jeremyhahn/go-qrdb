//go:build qrdb_vector

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
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Typed errors
// ---------------------------------------------------------------------------

// VectorParseError is returned when a vector string cannot be parsed.
type VectorParseError struct {
	Value string
	Err   error
}

// Error returns a human-readable description.
func (e *VectorParseError) Error() string {
	return "invalid vector component: " + e.Value + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *VectorParseError) Unwrap() error { return e.Err }

// JSONParseError is returned when JSON input cannot be parsed.
type JSONParseError struct {
	Input string
	Err   error
}

// Error returns a human-readable description.
func (e *JSONParseError) Error() string {
	return "invalid JSON: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *JSONParseError) Unwrap() error { return e.Err }

// InvalidDistanceMetricError is returned for unknown distance metric strings.
type InvalidDistanceMetricError struct {
	Value string
}

// Error returns a human-readable description.
func (e *InvalidDistanceMetricError) Error() string {
	return "invalid distance metric: " + e.Value
}

// InvalidIndexTypeError is returned for unknown index type strings.
type InvalidIndexTypeError struct {
	Value string
}

// Error returns a human-readable description.
func (e *InvalidIndexTypeError) Error() string {
	return "invalid index type: " + e.Value
}

// InvalidConsistencyLevelError is returned for unknown consistency level strings.
type InvalidConsistencyLevelError struct {
	Value string
}

// Error returns a human-readable description.
func (e *InvalidConsistencyLevelError) Error() string {
	return "invalid consistency level: " + e.Value
}

// InvalidVectorIDError is returned when a vector ID cannot be parsed as uint64.
type InvalidVectorIDError struct {
	Input string
}

// Error returns a human-readable description.
func (e *InvalidVectorIDError) Error() string {
	return "invalid vector ID: " + e.Input + ": must be a positive integer"
}

// ---------------------------------------------------------------------------
// Map-based dispatch tables
// ---------------------------------------------------------------------------

// distanceMetricMap maps CLI distance metric strings to transport constants.
var distanceMetricMap = map[string]transport.DistanceMetric{
	"cosine":      transport.DistanceCosine,
	"euclidean":   transport.DistanceEuclidean,
	"dot-product": transport.DistanceDotProduct,
	"manhattan":   transport.DistanceManhattan,
}

// indexTypeMap maps CLI index type strings to transport constants.
var indexTypeMap = map[string]transport.VectorIndexType{
	"hnsw":     transport.IndexHNSW,
	"flat":     transport.IndexFlat,
	"ivf-flat": transport.IndexIVFFlat,
}

// consistencyLevelMap maps CLI consistency level strings to uint8 values.
var consistencyLevelMap = map[string]uint8{
	"":        0,
	"default": 0,
	"leader":  1,
	"any":     2,
}

// distanceMetricNames maps transport constants back to display strings.
var distanceMetricNames = map[transport.DistanceMetric]string{
	transport.DistanceCosine:     "cosine",
	transport.DistanceEuclidean:  "euclidean",
	transport.DistanceDotProduct: "dot-product",
	transport.DistanceManhattan:  "manhattan",
}

// indexTypeNames maps transport constants back to display strings.
var indexTypeNames = map[transport.VectorIndexType]string{
	transport.IndexHNSW:    "hnsw",
	transport.IndexFlat:    "flat",
	transport.IndexIVFFlat: "ivf-flat",
}

// ---------------------------------------------------------------------------
// Parsing helpers
// ---------------------------------------------------------------------------

// parseFloatVector parses a comma-separated string of float values into
// a []float32 slice.
func parseFloatVector(s string) ([]float32, error) {
	parts := strings.Split(s, ",")
	vec := make([]float32, 0, len(parts))
	for _, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 32)
		if err != nil {
			return nil, &VectorParseError{Value: p, Err: err}
		}
		vec = append(vec, float32(f))
	}
	return vec, nil
}

// parseJSONMap parses a JSON string into a map[string]interface{}.
func parseJSONMap(s string) (map[string]interface{}, error) {
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, &JSONParseError{Input: s, Err: err}
	}
	return m, nil
}

// parseDistanceMetric converts a string to a transport.DistanceMetric.
func parseDistanceMetric(s string) (transport.DistanceMetric, error) {
	dm, ok := distanceMetricMap[s]
	if !ok {
		return "", &InvalidDistanceMetricError{Value: s}
	}
	return dm, nil
}

// parseIndexType converts a string to a transport.VectorIndexType.
func parseIndexType(s string) (transport.VectorIndexType, error) {
	it, ok := indexTypeMap[s]
	if !ok {
		return "", &InvalidIndexTypeError{Value: s}
	}
	return it, nil
}

// parseConsistencyLevel converts a string to a uint8 consistency level.
func parseConsistencyLevel(s string) (uint8, error) {
	cl, ok := consistencyLevelMap[s]
	if !ok {
		return 0, &InvalidConsistencyLevelError{Value: s}
	}
	return cl, nil
}

// parseVectorID parses a string vector ID into a uint64.
func parseVectorID(s string) (uint64, error) {
	id, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, &InvalidVectorIDError{Input: s}
	}
	return id, nil
}

// parseVectorIDs parses a comma-separated string of vector IDs into a []uint64 slice.
func parseVectorIDs(s string) ([]uint64, error) {
	parts := strings.Split(s, ",")
	ids := make([]uint64, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed == "" {
			continue
		}
		id, err := parseVectorID(trimmed)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// ---------------------------------------------------------------------------
// Root command
// ---------------------------------------------------------------------------

// newVectorCmd returns the vector command tree when the vector feature is
// compiled in.
func newVectorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vector",
		Short: "Vector database operations",
		Long:  "Manage vector namespaces, perform CRUD operations on vectors, execute similarity searches, and maintain indexes.",
	}

	// Namespace management subgroup.
	nsCmd := &cobra.Command{
		Use:   "namespace",
		Short: "Vector namespace management",
	}
	nsCmd.AddCommand(
		newVectorNamespaceCreateCmd(),
		newVectorNamespaceUpdateCmd(),
		newVectorNamespaceDeleteCmd(),
		newVectorNamespaceListCmd(),
		newVectorNamespaceInfoCmd(),
	)
	cmd.AddCommand(nsCmd)

	// Vector CRUD commands.
	cmd.AddCommand(newVectorUpsertCmd())
	cmd.AddCommand(newVectorGetCmd())
	cmd.AddCommand(newVectorDeleteCmd())
	cmd.AddCommand(newVectorBatchUpsertCmd())
	cmd.AddCommand(newVectorBatchDeleteCmd())
	cmd.AddCommand(newVectorListCmd())
	cmd.AddCommand(newVectorUpdateMetadataCmd())

	// Search commands.
	cmd.AddCommand(newVectorSearchCmd())
	cmd.AddCommand(newVectorSearchMetadataCmd())
	cmd.AddCommand(newVectorSearchHybridCmd())
	cmd.AddCommand(newVectorSearchByIDCmd())

	// Index maintenance commands.
	cmd.AddCommand(newVectorRebuildIndexCmd())
	cmd.AddCommand(newVectorRebuildProgressCmd())
	cmd.AddCommand(newVectorIndexStatsCmd())

	// Compaction commands.
	cmd.AddCommand(newVectorCompactCmd())
	cmd.AddCommand(newVectorCompactProgressCmd())

	return cmd
}

// ---------------------------------------------------------------------------
// Namespace commands
// ---------------------------------------------------------------------------

// newVectorNamespaceCreateCmd creates the "vector namespace create" subcommand.
func newVectorNamespaceCreateCmd() *cobra.Command {
	var (
		namespace      string
		dimension      uint32
		distanceMetric string
		indexType      string
		shardID        uint64
	)

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new vector namespace",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dm, err := parseDistanceMetric(distanceMetric)
			if err != nil {
				return err
			}
			it, err := parseIndexType(indexType)
			if err != nil {
				return err
			}

			req := &transport.CreateNamespaceRequest{
				Namespace:      namespace,
				Dimension:      dimension,
				DistanceMetric: dm,
				IndexType:      it,
				ShardID:        shardID,
			}

			resp, err := client.Vector().CreateNamespace(cmd.Context(), req)
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			default:
				_, _ = fmt.Fprintf(outputWriter, "Success:   %t\n", resp.Success)
				_, _ = fmt.Fprintf(outputWriter, "Timestamp: %d\n", resp.Timestamp)
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&namespace, "namespace", "", "Namespace name")
	cmd.Flags().Uint32Var(&dimension, "dimension", 0, "Vector dimensionality")
	cmd.Flags().StringVar(&distanceMetric, "distance-metric", "cosine", "Distance metric: cosine, euclidean, dot-product, manhattan")
	cmd.Flags().StringVar(&indexType, "index-type", "hnsw", "Index type: hnsw, flat, ivf-flat")
	cmd.Flags().Uint64Var(&shardID, "shard-id", 0, "Target shard ID")
	_ = cmd.MarkFlagRequired("namespace")
	_ = cmd.MarkFlagRequired("dimension")

	return cmd
}

func newVectorNamespaceUpdateCmd() *cobra.Command {
	var (
		namespace   string
		dimension   uint32
		indexParams map[string]string
		metadataStr string
	)

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update a vector namespace's index parameters and metadata",
		Long: "Update a vector namespace's index parameters and metadata in place, keeping every vector. " +
			"Each key given is merged over the stored value; a key not given keeps its value. " +
			"The dimension cannot change; --dimension, when given, must equal the namespace's. " +
			"A change to an index parameter the built index depends on reports that the index needs a rebuild.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req := &transport.UpdateNamespaceRequest{
				Namespace: namespace,
				Dimension: dimension,
			}
			if len(indexParams) > 0 {
				req.IndexConfig = indexParams
			}
			if metadataStr != "" {
				md, err := parseJSONMap(metadataStr)
				if err != nil {
					return err
				}
				req.Metadata = md
			}

			resp, err := client.Vector().UpdateNamespace(cmd.Context(), req)
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			default:
				_, _ = fmt.Fprintf(outputWriter, "Success:          %t\n", resp.Success)
				_, _ = fmt.Fprintf(outputWriter, "Requires rebuild: %t\n", resp.RequiresRebuild)
				_, _ = fmt.Fprintf(outputWriter, "Timestamp:        %d\n", resp.Timestamp)
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&namespace, "namespace", "", "Namespace name")
	cmd.Flags().Uint32Var(&dimension, "dimension", 0, "Expected vector dimensionality (must equal the namespace's)")
	cmd.Flags().StringToStringVar(&indexParams, "index-param", nil, "Index parameter key=value (repeatable)")
	cmd.Flags().StringVar(&metadataStr, "metadata", "", "JSON metadata object merged over the namespace's metadata")
	_ = cmd.MarkFlagRequired("namespace")

	return cmd
}

// newVectorNamespaceDeleteCmd creates the "vector namespace delete" subcommand.
func newVectorNamespaceDeleteCmd() *cobra.Command {
	var (
		namespace string
		shardID   uint64
	)

	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete a vector namespace",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req := &transport.DeleteNamespaceRequest{
				Namespace: namespace,
				ShardID:   shardID,
			}
			resp, err := client.Vector().DeleteNamespace(cmd.Context(), req)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(outputWriter, "Success:        %t\n", resp.Success)
			_, _ = fmt.Fprintf(outputWriter, "VectorsDeleted: %d\n", resp.VectorsDeleted)
			_, _ = fmt.Fprintf(outputWriter, "Timestamp:      %d\n", resp.Timestamp)
			return nil
		},
	}

	cmd.Flags().StringVar(&namespace, "namespace", "", "Namespace name")
	cmd.Flags().Uint64Var(&shardID, "shard-id", 0, "Target shard ID")
	_ = cmd.MarkFlagRequired("namespace")

	return cmd
}

// newVectorNamespaceListCmd creates the "vector namespace list" subcommand.
func newVectorNamespaceListCmd() *cobra.Command {
	var shardID uint64

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all vector namespaces",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req := &transport.ListNamespacesRequest{
				ShardID: shardID,
			}
			resp, err := client.Vector().ListNamespaces(cmd.Context(), req)
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			default:
				for _, ns := range resp.Namespaces {
					_, _ = fmt.Fprintln(outputWriter, ns.Namespace)
				}
				return nil
			}
		},
	}

	cmd.Flags().Uint64Var(&shardID, "shard-id", 0, "Target shard ID")

	return cmd
}

// newVectorNamespaceInfoCmd creates the "vector namespace info" subcommand.
func newVectorNamespaceInfoCmd() *cobra.Command {
	var (
		namespace string
		shardID   uint64
	)

	cmd := &cobra.Command{
		Use:   "info",
		Short: "Show namespace metadata",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req := &transport.GetNamespaceInfoRequest{
				Namespace: namespace,
				ShardID:   shardID,
			}
			info, err := client.Vector().GetNamespaceInfo(cmd.Context(), req)
			if err != nil {
				return err
			}

			return printNamespaceInfo(info)
		},
	}

	cmd.Flags().StringVar(&namespace, "namespace", "", "Namespace name")
	cmd.Flags().Uint64Var(&shardID, "shard-id", 0, "Target shard ID")
	_ = cmd.MarkFlagRequired("namespace")

	return cmd
}

// ---------------------------------------------------------------------------
// Vector CRUD commands
// ---------------------------------------------------------------------------

// newVectorUpsertCmd creates the "vector upsert" subcommand.
func newVectorUpsertCmd() *cobra.Command {
	var (
		namespace   string
		idStr       string
		vectorStr   string
		metadataStr string
		shardID     uint64
	)

	cmd := &cobra.Command{
		Use:   "upsert",
		Short: "Insert or update a vector",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			id, err := parseVectorID(idStr)
			if err != nil {
				return err
			}
			vec, err := parseFloatVector(vectorStr)
			if err != nil {
				return err
			}

			req := &transport.UpsertVectorRequest{
				Namespace: namespace,
				ID:        id,
				Vector:    vec,
				ShardID:   shardID,
			}

			if metadataStr != "" {
				md, err := parseJSONMap(metadataStr)
				if err != nil {
					return err
				}
				req.Metadata = md
			}

			resp, err := client.Vector().UpsertVector(cmd.Context(), req)
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			default:
				_, _ = fmt.Fprintf(outputWriter, "ID:        %d\n", resp.ID)
				_, _ = fmt.Fprintf(outputWriter, "Version:   %d\n", resp.Version)
				_, _ = fmt.Fprintf(outputWriter, "Success:   %t\n", resp.Success)
				_, _ = fmt.Fprintf(outputWriter, "Timestamp: %d\n", resp.Timestamp)
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&namespace, "namespace", "", "Namespace name")
	cmd.Flags().StringVar(&idStr, "id", "", "Vector ID (uint64)")
	cmd.Flags().StringVar(&vectorStr, "vector", "", "Comma-separated float values")
	cmd.Flags().StringVar(&metadataStr, "metadata", "", "JSON metadata object")
	cmd.Flags().Uint64Var(&shardID, "shard-id", 0, "Target shard ID")
	_ = cmd.MarkFlagRequired("namespace")
	_ = cmd.MarkFlagRequired("id")
	_ = cmd.MarkFlagRequired("vector")

	return cmd
}

// newVectorGetCmd creates the "vector get" subcommand.
func newVectorGetCmd() *cobra.Command {
	var (
		namespace   string
		idStr       string
		consistency string
		shardID     uint64
	)

	cmd := &cobra.Command{
		Use:   "get",
		Short: "Retrieve a vector by ID",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			id, err := parseVectorID(idStr)
			if err != nil {
				return err
			}
			cl, err := parseConsistencyLevel(consistency)
			if err != nil {
				return err
			}

			req := &transport.GetVectorRequest{
				Namespace:   namespace,
				ID:          id,
				Consistency: cl,
				ShardID:     shardID,
			}

			resp, err := client.Vector().GetVector(cmd.Context(), req)
			if err != nil {
				return err
			}

			return printGetVectorResponse(resp)
		},
	}

	cmd.Flags().StringVar(&namespace, "namespace", "", "Namespace name")
	cmd.Flags().StringVar(&idStr, "id", "", "Vector ID (uint64)")
	cmd.Flags().StringVar(&consistency, "consistency", "", "Consistency level: default, leader, any")
	cmd.Flags().Uint64Var(&shardID, "shard-id", 0, "Target shard ID")
	_ = cmd.MarkFlagRequired("namespace")
	_ = cmd.MarkFlagRequired("id")

	return cmd
}

// newVectorDeleteCmd creates the "vector delete" subcommand.
func newVectorDeleteCmd() *cobra.Command {
	var (
		namespace string
		idStr     string
		shardID   uint64
	)

	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete a vector by ID",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			id, err := parseVectorID(idStr)
			if err != nil {
				return err
			}
			req := &transport.DeleteVectorRequest{
				Namespace: namespace,
				ID:        id,
				ShardID:   shardID,
			}
			resp, err := client.Vector().DeleteVector(cmd.Context(), req)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(outputWriter, "Success:   %t\n", resp.Success)
			_, _ = fmt.Fprintf(outputWriter, "Timestamp: %d\n", resp.Timestamp)
			return nil
		},
	}

	cmd.Flags().StringVar(&namespace, "namespace", "", "Namespace name")
	cmd.Flags().StringVar(&idStr, "id", "", "Vector ID (uint64)")
	cmd.Flags().Uint64Var(&shardID, "shard-id", 0, "Target shard ID")
	_ = cmd.MarkFlagRequired("namespace")
	_ = cmd.MarkFlagRequired("id")

	return cmd
}

// newVectorBatchUpsertCmd creates the "vector batch-upsert" subcommand.
func newVectorBatchUpsertCmd() *cobra.Command {
	var (
		namespace string
		file      string
		shardID   uint64
	)

	cmd := &cobra.Command{
		Use:   "batch-upsert",
		Short: "Bulk upsert vectors from a JSON file",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
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

			var vectors []*transport.VectorData
			if err := json.Unmarshal(data, &vectors); err != nil {
				return &JSONParseError{Input: file, Err: err}
			}

			req := &transport.BatchUpsertRequest{
				Namespace: namespace,
				Vectors:   vectors,
				ShardID:   shardID,
			}

			resp, err := client.Vector().BatchUpsert(cmd.Context(), req)
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			default:
				_, _ = fmt.Fprintf(outputWriter, "Success:       %t\n", resp.Success)
				_, _ = fmt.Fprintf(outputWriter, "InsertedCount: %d\n", resp.InsertedCount)
				_, _ = fmt.Fprintf(outputWriter, "Timestamp:     %d\n", resp.Timestamp)
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&namespace, "namespace", "", "Namespace name")
	cmd.Flags().StringVar(&file, "file", "", "JSON file path (use \"-\" for stdin)")
	cmd.Flags().Uint64Var(&shardID, "shard-id", 0, "Target shard ID")
	_ = cmd.MarkFlagRequired("namespace")
	_ = cmd.MarkFlagRequired("file")

	return cmd
}

// newVectorBatchDeleteCmd creates the "vector batch-delete" subcommand.
func newVectorBatchDeleteCmd() *cobra.Command {
	var (
		namespace string
		idsStr    string
		shardID   uint64
	)

	cmd := &cobra.Command{
		Use:   "batch-delete",
		Short: "Bulk delete vectors by ID",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ids, err := parseVectorIDs(idsStr)
			if err != nil {
				return err
			}

			req := &transport.BatchDeleteVectorsRequest{
				Namespace: namespace,
				IDs:       ids,
				ShardID:   shardID,
			}

			resp, err := client.Vector().BatchDeleteVectors(cmd.Context(), req)
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			default:
				_, _ = fmt.Fprintf(outputWriter, "Success:      %t\n", resp.Success)
				_, _ = fmt.Fprintf(outputWriter, "DeletedCount: %d\n", resp.DeletedCount)
				_, _ = fmt.Fprintf(outputWriter, "Timestamp:    %d\n", resp.Timestamp)
				if len(resp.FailedIDs) > 0 {
					for _, fid := range resp.FailedIDs {
						_, _ = fmt.Fprintf(outputWriter, "  FailedID: %d\n", fid)
					}
				}
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&namespace, "namespace", "", "Namespace name")
	cmd.Flags().StringVar(&idsStr, "ids", "", "Comma-separated vector IDs (uint64)")
	cmd.Flags().Uint64Var(&shardID, "shard-id", 0, "Target shard ID")
	_ = cmd.MarkFlagRequired("namespace")
	_ = cmd.MarkFlagRequired("ids")

	return cmd
}

// newVectorListCmd creates the "vector list" subcommand.
func newVectorListCmd() *cobra.Command {
	var (
		namespace string
		limit     int
		cursor    string
		shardID   uint64
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List vectors in a namespace",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req := &transport.ListVectorsRequest{
				Namespace: namespace,
				Limit:     limit,
				Cursor:    cursor,
				ShardID:   shardID,
			}

			resp, err := client.Vector().ListVectors(cmd.Context(), req)
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			default:
				headers := []string{"ID", "VERSION", "CREATED_AT", "UPDATED_AT"}
				rows := make([][]string, 0, len(resp.Vectors))
				for _, v := range resp.Vectors {
					rows = append(rows, []string{
						strconv.FormatUint(v.ID, 10),
						strconv.FormatUint(v.Version, 10),
						strconv.FormatInt(v.CreatedAt, 10),
						strconv.FormatInt(v.UpdatedAt, 10),
					})
				}
				printTable(headers, rows)
				if resp.NextCursor != "" {
					_, _ = fmt.Fprintf(outputWriter, "Next cursor: %s\n", resp.NextCursor)
				}
				_, _ = fmt.Fprintf(outputWriter, "Total: %d\n", resp.TotalCount)
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&namespace, "namespace", "", "Namespace name")
	cmd.Flags().IntVar(&limit, "limit", 0, "Maximum number of vectors to return")
	cmd.Flags().StringVar(&cursor, "cursor", "", "Pagination cursor")
	cmd.Flags().Uint64Var(&shardID, "shard-id", 0, "Target shard ID")
	_ = cmd.MarkFlagRequired("namespace")

	return cmd
}

// newVectorUpdateMetadataCmd creates the "vector update-metadata" subcommand.
func newVectorUpdateMetadataCmd() *cobra.Command {
	var (
		namespace   string
		idStr       string
		metadataStr string
		shardID     uint64
	)

	cmd := &cobra.Command{
		Use:   "update-metadata",
		Short: "Update metadata for a vector",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			id, err := parseVectorID(idStr)
			if err != nil {
				return err
			}
			md, err := parseJSONMap(metadataStr)
			if err != nil {
				return err
			}

			req := &transport.UpdateVectorMetadataRequest{
				Namespace: namespace,
				ID:        id,
				Metadata:  md,
				ShardID:   shardID,
			}

			resp, err := client.Vector().UpdateMetadata(cmd.Context(), req)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(outputWriter, "ID:        %d\n", resp.ID)
			_, _ = fmt.Fprintf(outputWriter, "Version:   %d\n", resp.Version)
			_, _ = fmt.Fprintf(outputWriter, "Success:   %t\n", resp.Success)
			_, _ = fmt.Fprintf(outputWriter, "Timestamp: %d\n", resp.Timestamp)
			return nil
		},
	}

	cmd.Flags().StringVar(&namespace, "namespace", "", "Namespace name")
	cmd.Flags().StringVar(&idStr, "id", "", "Vector ID (uint64)")
	cmd.Flags().StringVar(&metadataStr, "metadata", "", "JSON metadata object")
	cmd.Flags().Uint64Var(&shardID, "shard-id", 0, "Target shard ID")
	_ = cmd.MarkFlagRequired("namespace")
	_ = cmd.MarkFlagRequired("id")
	_ = cmd.MarkFlagRequired("metadata")

	return cmd
}

// ---------------------------------------------------------------------------
// Search commands
// ---------------------------------------------------------------------------

// newVectorSearchCmd creates the "vector search" subcommand.
func newVectorSearchCmd() *cobra.Command {
	var (
		namespace   string
		vectorStr   string
		k           int
		efSearch    int
		minScore    float64
		consistency string
		shardID     uint64
	)

	cmd := &cobra.Command{
		Use:   "search",
		Short: "k-NN similarity search",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			vec, err := parseFloatVector(vectorStr)
			if err != nil {
				return err
			}
			cl, err := parseConsistencyLevel(consistency)
			if err != nil {
				return err
			}

			req := &transport.SearchSimilarRequest{
				Namespace:   namespace,
				QueryVector: vec,
				K:           k,
				EfSearch:    efSearch,
				MinScore:    float32(minScore),
				Consistency: cl,
				ShardID:     shardID,
			}

			resp, err := client.Vector().SearchSimilar(cmd.Context(), req)
			if err != nil {
				return err
			}

			return printSearchResults(resp.Results, resp.QueryDuration)
		},
	}

	cmd.Flags().StringVar(&namespace, "namespace", "", "Namespace name")
	cmd.Flags().StringVar(&vectorStr, "vector", "", "Comma-separated float query vector")
	cmd.Flags().IntVar(&k, "k", 0, "Number of nearest neighbors")
	cmd.Flags().IntVar(&efSearch, "ef-search", 0, "HNSW ef parameter for search")
	cmd.Flags().Float64Var(&minScore, "min-score", 0, "Minimum similarity score threshold")
	cmd.Flags().StringVar(&consistency, "consistency", "", "Consistency level: default, leader, any")
	cmd.Flags().Uint64Var(&shardID, "shard-id", 0, "Target shard ID")
	_ = cmd.MarkFlagRequired("namespace")
	_ = cmd.MarkFlagRequired("vector")
	_ = cmd.MarkFlagRequired("k")

	return cmd
}

// newVectorSearchMetadataCmd creates the "vector search-metadata" subcommand.
func newVectorSearchMetadataCmd() *cobra.Command {
	var (
		namespace   string
		filtersStr  string
		limit       int
		offset      int
		consistency string
		shardID     uint64
	)

	cmd := &cobra.Command{
		Use:   "search-metadata",
		Short: "Search vectors by metadata filters",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			filters, err := parseJSONMap(filtersStr)
			if err != nil {
				return err
			}
			cl, err := parseConsistencyLevel(consistency)
			if err != nil {
				return err
			}

			req := &transport.SearchByMetadataRequest{
				Namespace:   namespace,
				Filters:     filters,
				Limit:       limit,
				Offset:      offset,
				Consistency: cl,
				ShardID:     shardID,
			}

			resp, err := client.Vector().SearchByMetadata(cmd.Context(), req)
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			default:
				headers := []string{"ID", "VERSION", "CREATED_AT", "UPDATED_AT"}
				rows := make([][]string, 0, len(resp.Results))
				for _, v := range resp.Results {
					rows = append(rows, []string{
						strconv.FormatUint(v.ID, 10),
						strconv.FormatUint(v.Version, 10),
						strconv.FormatInt(v.CreatedAt, 10),
						strconv.FormatInt(v.UpdatedAt, 10),
					})
				}
				printTable(headers, rows)
				_, _ = fmt.Fprintf(outputWriter, "Total: %d\n", resp.TotalCount)
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&namespace, "namespace", "", "Namespace name")
	cmd.Flags().StringVar(&filtersStr, "filters", "", "JSON metadata filter object")
	cmd.Flags().IntVar(&limit, "limit", 0, "Maximum number of results")
	cmd.Flags().IntVar(&offset, "offset", 0, "Number of results to skip")
	cmd.Flags().StringVar(&consistency, "consistency", "", "Consistency level: default, leader, any")
	cmd.Flags().Uint64Var(&shardID, "shard-id", 0, "Target shard ID")
	_ = cmd.MarkFlagRequired("namespace")
	_ = cmd.MarkFlagRequired("filters")

	return cmd
}

// newVectorSearchHybridCmd creates the "vector search-hybrid" subcommand.
func newVectorSearchHybridCmd() *cobra.Command {
	var (
		namespace   string
		vectorStr   string
		filtersStr  string
		k           int
		efSearch    int
		minScore    float64
		consistency string
		shardID     uint64
	)

	cmd := &cobra.Command{
		Use:   "search-hybrid",
		Short: "Combined vector similarity and metadata search",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			vec, err := parseFloatVector(vectorStr)
			if err != nil {
				return err
			}
			filters, err := parseJSONMap(filtersStr)
			if err != nil {
				return err
			}
			cl, err := parseConsistencyLevel(consistency)
			if err != nil {
				return err
			}

			req := &transport.HybridSearchRequest{
				Namespace:      namespace,
				QueryVector:    vec,
				MetadataFilter: filters,
				K:              k,
				EfSearch:       efSearch,
				MinScore:       float32(minScore),
				Consistency:    cl,
				ShardID:        shardID,
			}

			resp, err := client.Vector().HybridSearch(cmd.Context(), req)
			if err != nil {
				return err
			}

			return printSearchResults(resp.Results, resp.QueryDuration)
		},
	}

	cmd.Flags().StringVar(&namespace, "namespace", "", "Namespace name")
	cmd.Flags().StringVar(&vectorStr, "vector", "", "Comma-separated float query vector")
	cmd.Flags().StringVar(&filtersStr, "filters", "", "JSON metadata filter object")
	cmd.Flags().IntVar(&k, "k", 0, "Number of nearest neighbors")
	cmd.Flags().IntVar(&efSearch, "ef-search", 0, "HNSW ef parameter for search")
	cmd.Flags().Float64Var(&minScore, "min-score", 0, "Minimum similarity score threshold")
	cmd.Flags().StringVar(&consistency, "consistency", "", "Consistency level: default, leader, any")
	cmd.Flags().Uint64Var(&shardID, "shard-id", 0, "Target shard ID")
	_ = cmd.MarkFlagRequired("namespace")
	_ = cmd.MarkFlagRequired("vector")
	_ = cmd.MarkFlagRequired("filters")
	_ = cmd.MarkFlagRequired("k")

	return cmd
}

// newVectorSearchByIDCmd creates the "vector search-by-id" subcommand.
func newVectorSearchByIDCmd() *cobra.Command {
	var (
		namespace   string
		idStr       string
		k           int
		consistency string
		shardID     uint64
	)

	cmd := &cobra.Command{
		Use:   "search-by-id",
		Short: "Search similar vectors using an existing vector's ID",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			id, err := parseVectorID(idStr)
			if err != nil {
				return err
			}
			cl, err := parseConsistencyLevel(consistency)
			if err != nil {
				return err
			}

			req := &transport.SearchByIDRequest{
				Namespace:   namespace,
				ID:          id,
				K:           k,
				Consistency: cl,
				ShardID:     shardID,
			}

			resp, err := client.Vector().SearchByID(cmd.Context(), req)
			if err != nil {
				return err
			}

			return printSearchResults(resp.Results, resp.QueryDuration)
		},
	}

	cmd.Flags().StringVar(&namespace, "namespace", "", "Namespace name")
	cmd.Flags().StringVar(&idStr, "id", "", "Source vector ID (uint64)")
	cmd.Flags().IntVar(&k, "k", 0, "Number of nearest neighbors")
	cmd.Flags().StringVar(&consistency, "consistency", "", "Consistency level: default, leader, any")
	cmd.Flags().Uint64Var(&shardID, "shard-id", 0, "Target shard ID")
	_ = cmd.MarkFlagRequired("namespace")
	_ = cmd.MarkFlagRequired("id")
	_ = cmd.MarkFlagRequired("k")

	return cmd
}

// ---------------------------------------------------------------------------
// Index maintenance commands
// ---------------------------------------------------------------------------

// newVectorRebuildIndexCmd creates the "vector rebuild-index" subcommand.
func newVectorRebuildIndexCmd() *cobra.Command {
	var (
		namespace string
		shardID   uint64
	)

	cmd := &cobra.Command{
		Use:   "rebuild-index",
		Short: "Trigger an asynchronous index rebuild",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req := &transport.RebuildIndexRequest{
				Namespace: namespace,
				ShardID:   shardID,
			}

			resp, err := client.Vector().RebuildIndex(cmd.Context(), req)
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			default:
				_, _ = fmt.Fprintf(outputWriter, "JobID:     %s\n", resp.JobID)
				_, _ = fmt.Fprintf(outputWriter, "Status:    %s\n", resp.Status)
				_, _ = fmt.Fprintf(outputWriter, "Success:   %t\n", resp.Success)
				_, _ = fmt.Fprintf(outputWriter, "Timestamp: %d\n", resp.Timestamp)
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&namespace, "namespace", "", "Namespace name")
	cmd.Flags().Uint64Var(&shardID, "shard-id", 0, "Target shard ID")
	_ = cmd.MarkFlagRequired("namespace")

	return cmd
}

// newVectorRebuildProgressCmd creates the "vector rebuild-progress" subcommand.
func newVectorRebuildProgressCmd() *cobra.Command {
	var (
		namespace string
		jobID     string
	)

	cmd := &cobra.Command{
		Use:   "rebuild-progress",
		Short: "Query index rebuild progress",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req := &transport.GetRebuildProgressRequest{
				Namespace: namespace,
				JobID:     jobID,
			}

			progress, err := client.Vector().GetRebuildProgress(cmd.Context(), req)
			if err != nil {
				return err
			}

			return printRebuildProgress(progress)
		},
	}

	cmd.Flags().StringVar(&namespace, "namespace", "", "Namespace name")
	cmd.Flags().StringVar(&jobID, "job-id", "", "Rebuild job ID")
	_ = cmd.MarkFlagRequired("namespace")
	_ = cmd.MarkFlagRequired("job-id")

	return cmd
}

// newVectorIndexStatsCmd creates the "vector index-stats" subcommand.
func newVectorIndexStatsCmd() *cobra.Command {
	var (
		namespace string
		shardID   uint64
	)

	cmd := &cobra.Command{
		Use:   "index-stats",
		Short: "Show index statistics for a namespace",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req := &transport.GetIndexStatsRequest{
				Namespace: namespace,
				ShardID:   shardID,
			}

			stats, err := client.Vector().GetIndexStats(cmd.Context(), req)
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(stats)
			case "yaml":
				return printYAML(stats)
			default:
				_, _ = fmt.Fprintf(outputWriter, "Namespace:        %s\n", stats.Namespace)
				_, _ = fmt.Fprintf(outputWriter, "VectorCount:      %d\n", stats.VectorCount)
				_, _ = fmt.Fprintf(outputWriter, "Dimension:        %d\n", stats.Dimension)
				_, _ = fmt.Fprintf(outputWriter, "IndexType:        %s\n", indexTypeName(stats.IndexType))
				_, _ = fmt.Fprintf(outputWriter, "IndexSizeBytes:   %d\n", stats.IndexSizeBytes)
				_, _ = fmt.Fprintf(outputWriter, "MemoryUsageBytes: %d\n", stats.MemoryUsageBytes)
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&namespace, "namespace", "", "Namespace name")
	cmd.Flags().Uint64Var(&shardID, "shard-id", 0, "Target shard ID")
	_ = cmd.MarkFlagRequired("namespace")

	return cmd
}

// ---------------------------------------------------------------------------
// Compaction commands
// ---------------------------------------------------------------------------

// newVectorCompactCmd creates the "vector compact" subcommand.
func newVectorCompactCmd() *cobra.Command {
	var (
		namespace string
		shardID   uint64
	)

	cmd := &cobra.Command{
		Use:   "compact",
		Short: "Trigger compaction of a namespace's storage",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req := &transport.CompactNamespaceRequest{
				Namespace: namespace,
				ShardID:   shardID,
			}

			resp, err := client.Vector().CompactNamespace(cmd.Context(), req)
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			default:
				_, _ = fmt.Fprintf(outputWriter, "OperationID: %s\n", resp.OperationID)
				_, _ = fmt.Fprintf(outputWriter, "Success:     %t\n", resp.Success)
				_, _ = fmt.Fprintf(outputWriter, "StartedAt:   %d\n", resp.StartedAt)
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&namespace, "namespace", "", "Namespace name")
	cmd.Flags().Uint64Var(&shardID, "shard-id", 0, "Target shard ID")
	_ = cmd.MarkFlagRequired("namespace")

	return cmd
}

// newVectorCompactProgressCmd creates the "vector compact-progress" subcommand.
func newVectorCompactProgressCmd() *cobra.Command {
	var (
		namespace   string
		operationID string
		shardID     uint64
	)

	cmd := &cobra.Command{
		Use:   "compact-progress",
		Short: "Query compaction progress",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req := &transport.GetCompactionProgressRequest{
				Namespace:   namespace,
				OperationID: operationID,
				ShardID:     shardID,
			}

			progress, err := client.Vector().GetCompactionProgress(cmd.Context(), req)
			if err != nil {
				return err
			}

			return printCompactionProgress(progress)
		},
	}

	cmd.Flags().StringVar(&namespace, "namespace", "", "Namespace name")
	cmd.Flags().StringVar(&operationID, "operation-id", "", "Compaction operation ID")
	cmd.Flags().Uint64Var(&shardID, "shard-id", 0, "Target shard ID")
	_ = cmd.MarkFlagRequired("namespace")
	_ = cmd.MarkFlagRequired("operation-id")

	return cmd
}

// ---------------------------------------------------------------------------
// Output helpers
// ---------------------------------------------------------------------------

// printNamespaceInfo prints a GetNamespaceInfoResponse in the current output format.
func printNamespaceInfo(info *transport.GetNamespaceInfoResponse) error {
	switch outputFormat {
	case "json":
		return printJSON(info)
	case "yaml":
		return printYAML(info)
	default:
		_, _ = fmt.Fprintf(outputWriter, "Namespace:      %s\n", info.Namespace)
		_, _ = fmt.Fprintf(outputWriter, "Dimension:      %d\n", info.Dimension)
		_, _ = fmt.Fprintf(outputWriter, "DistanceMetric: %s\n", distanceMetricName(info.DistanceMetric))
		_, _ = fmt.Fprintf(outputWriter, "IndexType:      %s\n", indexTypeName(info.IndexType))
		_, _ = fmt.Fprintf(outputWriter, "VectorCount:    %d\n", info.VectorCount)
		_, _ = fmt.Fprintf(outputWriter, "CreatedAt:      %d\n", info.CreatedAt)
		_, _ = fmt.Fprintf(outputWriter, "UpdatedAt:      %d\n", info.UpdatedAt)
		return nil
	}
}

// printGetVectorResponse prints a GetVectorResponse in the current output format.
func printGetVectorResponse(resp *transport.GetVectorResponse) error {
	switch outputFormat {
	case "json":
		return printJSON(resp)
	case "yaml":
		return printYAML(resp)
	default:
		_, _ = fmt.Fprintf(outputWriter, "ID:        %d\n", resp.ID)
		_, _ = fmt.Fprintf(outputWriter, "Found:     %t\n", resp.Found)
		if len(resp.Vector) > 0 {
			_, _ = fmt.Fprintf(outputWriter, "Vector:    %s\n", formatVector(resp.Vector))
		}
		_, _ = fmt.Fprintf(outputWriter, "Version:   %d\n", resp.Version)
		_, _ = fmt.Fprintf(outputWriter, "CreatedAt: %d\n", resp.CreatedAt)
		_, _ = fmt.Fprintf(outputWriter, "UpdatedAt: %d\n", resp.UpdatedAt)
		if len(resp.Metadata) > 0 {
			md, _ := json.Marshal(resp.Metadata)
			_, _ = fmt.Fprintf(outputWriter, "Metadata:  %s\n", string(md))
		}
		return nil
	}
}

// printSearchResults prints vector search results in the current output format.
func printSearchResults(results []*transport.VectorSearchResult, queryDuration int64) error {
	switch outputFormat {
	case "json":
		return printJSON(map[string]interface{}{
			"results":        results,
			"query_duration": queryDuration,
		})
	case "yaml":
		return printYAML(map[string]interface{}{
			"results":        results,
			"query_duration": queryDuration,
		})
	default:
		headers := []string{"ID", "SCORE", "DISTANCE", "RANK"}
		rows := make([][]string, 0, len(results))
		for _, r := range results {
			rows = append(rows, []string{
				strconv.FormatUint(r.ID, 10),
				strconv.FormatFloat(float64(r.Score), 'f', -1, 32),
				strconv.FormatFloat(float64(r.Distance), 'f', -1, 32),
				strconv.Itoa(r.Rank),
			})
		}
		printTable(headers, rows)
		_, _ = fmt.Fprintf(outputWriter, "Query duration: %d ns\n", queryDuration)
		return nil
	}
}

// printRebuildProgress prints rebuild progress in the current output format.
func printRebuildProgress(progress *transport.GetRebuildProgressResponse) error {
	switch outputFormat {
	case "json":
		return printJSON(progress)
	case "yaml":
		return printYAML(progress)
	default:
		_, _ = fmt.Fprintf(outputWriter, "JobID:            %s\n", progress.JobID)
		_, _ = fmt.Fprintf(outputWriter, "Namespace:        %s\n", progress.Namespace)
		_, _ = fmt.Fprintf(outputWriter, "Status:           %s\n", progress.Status)
		_, _ = fmt.Fprintf(outputWriter, "PercentComplete:  %.2f%%\n", progress.PercentComplete)
		_, _ = fmt.Fprintf(outputWriter, "VectorsProcessed: %d\n", progress.VectorsProcessed)
		_, _ = fmt.Fprintf(outputWriter, "TotalVectors:     %d\n", progress.TotalVectors)
		_, _ = fmt.Fprintf(outputWriter, "StartedAt:        %d\n", progress.StartedAt)
		if progress.CompletedAt > 0 {
			_, _ = fmt.Fprintf(outputWriter, "CompletedAt:      %d\n", progress.CompletedAt)
		}
		if progress.Error != "" {
			_, _ = fmt.Fprintf(outputWriter, "Error:            %s\n", progress.Error)
		}
		return nil
	}
}

// printCompactionProgress prints compaction progress in the current output format.
func printCompactionProgress(progress *transport.GetCompactionProgressResponse) error {
	switch outputFormat {
	case "json":
		return printJSON(progress)
	case "yaml":
		return printYAML(progress)
	default:
		_, _ = fmt.Fprintf(outputWriter, "OperationID:     %s\n", progress.OperationID)
		_, _ = fmt.Fprintf(outputWriter, "Namespace:       %s\n", progress.Namespace)
		_, _ = fmt.Fprintf(outputWriter, "InProgress:      %t\n", progress.InProgress)
		_, _ = fmt.Fprintf(outputWriter, "PercentComplete: %.2f%%\n", progress.PercentComplete)
		_, _ = fmt.Fprintf(outputWriter, "BytesCompacted:  %d\n", progress.BytesCompacted)
		_, _ = fmt.Fprintf(outputWriter, "TotalBytes:      %d\n", progress.TotalBytes)
		_, _ = fmt.Fprintf(outputWriter, "StartedAt:       %d\n", progress.StartedAt)
		if progress.CompletedAt != nil {
			_, _ = fmt.Fprintf(outputWriter, "CompletedAt:     %d\n", *progress.CompletedAt)
		}
		if progress.Error != "" {
			_, _ = fmt.Fprintf(outputWriter, "Error:           %s\n", progress.Error)
		}
		return nil
	}
}

// formatVector converts a float32 slice to a comma-separated string.
func formatVector(vec []float32) string {
	parts := make([]string, len(vec))
	for i, v := range vec {
		parts[i] = strconv.FormatFloat(float64(v), 'f', -1, 32)
	}
	return strings.Join(parts, ",")
}

// distanceMetricName returns the display name for a DistanceMetric value.
func distanceMetricName(dm transport.DistanceMetric) string {
	if name, ok := distanceMetricNames[dm]; ok {
		return name
	}
	return dm
}

// indexTypeName returns the display name for a VectorIndexType value.
func indexTypeName(it transport.VectorIndexType) string {
	if name, ok := indexTypeNames[it]; ok {
		return name
	}
	return it
}
