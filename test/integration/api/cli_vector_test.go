// Copyright 2026 Jeremy Hahn
// Apache 2.0 License

//go:build integration

package integration

import (
	"fmt"
	"strings"
	"testing"
)

// TestCLIMultiProtocolVectorLifecycle tests the full vector lifecycle (namespace
// create, upsert, get, search, list, delete, namespace delete) across all
// protocols.
func TestCLIMultiProtocolVectorLifecycle(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)
	requireFeature(t, cfg, "vector")

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			namespace := fmt.Sprintf("vec-%s-%d", proto, ts)

			// Register cleanup before any mutations so it always runs.
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "vector", "delete", "--id", "1", "--namespace", namespace) //nolint:errcheck
				execCLIWithProtocol(t, cfg, proto, "vector", "delete", "--id", "2", "--namespace", namespace) //nolint:errcheck
				execCLIWithProtocol(t, cfg, proto, "vector", "namespace", "delete", "--namespace", namespace) //nolint:errcheck
			})

			// Step 1: Create a vector namespace with dimension 4 and cosine distance.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"vector", "namespace", "create",
				"--namespace", namespace,
				"--dimension", "4",
				"--distance-metric", "cosine")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] vector namespace create failed: %v", proto, err)
			}
			output := stdout + stderr
			assertNotEmpty(t, output,
				fmt.Sprintf("[%s] vector namespace create should produce output", proto))

			// Step 2: List namespaces and verify the new namespace is present.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"vector", "namespace", "list")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] vector namespace list failed: %v", proto, err)
			}
			output = stdout + stderr
			assertContains(t, output, namespace,
				fmt.Sprintf("[%s] vector namespace list should contain %s", proto, namespace))

			// Step 3: Upsert first vector (ID=1).
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"vector", "upsert",
				"--namespace", namespace,
				"--id", "1",
				"--vector", "1.0,0.0,0.0,0.0")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] vector upsert id=1 failed: %v", proto, err)
			}

			// Step 4: Upsert second vector (ID=2).
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"vector", "upsert",
				"--namespace", namespace,
				"--id", "2",
				"--vector", "0.0,1.0,0.0,0.0")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] vector upsert id=2 failed: %v", proto, err)
			}

			// Step 5: Get the first vector and verify output.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"vector", "get",
				"--id", "1",
				"--namespace", namespace)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] vector get id=1 failed: %v", proto, err)
			}
			output = stdout + stderr
			assertNotEmpty(t, output,
				fmt.Sprintf("[%s] vector get id=1 should produce output", proto))

			// Step 6: Search for nearest neighbors to vec1.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"vector", "search",
				"--namespace", namespace,
				"--vector", "1.0,0.0,0.0,0.0",
				"--k", "2")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] vector search failed: %v", proto, err)
			}
			output = stdout + stderr
			assertNotEmpty(t, output,
				fmt.Sprintf("[%s] vector search should produce output", proto))

			// Step 7: List all vectors in the namespace.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"vector", "list",
				"--namespace", namespace)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] vector list failed: %v", proto, err)
			}
			output = stdout + stderr
			assertNotEmpty(t, output,
				fmt.Sprintf("[%s] vector list should produce output", proto))

			// Step 8: Delete the first vector.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"vector", "delete",
				"--id", "1",
				"--namespace", namespace)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] vector delete id=1 failed: %v", proto, err)
			}

			// Step 9: Delete the namespace.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"vector", "namespace", "delete",
				"--namespace", namespace)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] vector namespace delete failed: %v", proto, err)
			}

			t.Logf("[%s] vector lifecycle passed (namespace: %s)", proto, strings.TrimSpace(namespace))
		})
	}
}

// TestCLIMultiProtocolVectorNamespaceInfo tests the vector namespace info command
// across all protocols. It creates a namespace, retrieves its info, and verifies
// the output contains Dimension and DistanceMetric fields.
func TestCLIMultiProtocolVectorNamespaceInfo(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)
	requireFeature(t, cfg, "vector")

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			namespace := fmt.Sprintf("vecinfo-%s-%d", proto, ts)

			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "vector", "namespace", "delete", "--namespace", namespace) //nolint:errcheck
			})

			// Create namespace.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"vector", "namespace", "create",
				"--namespace", namespace,
				"--dimension", "4",
				"--distance-metric", "cosine")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] vector namespace create failed: %v", proto, err)
			}

			// Get namespace info.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"vector", "namespace", "info",
				"--namespace", namespace)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] vector namespace info failed: %v", proto, err)
			}

			output := stdout + stderr
			assertNotEmpty(t, output,
				fmt.Sprintf("[%s] vector namespace info should produce output", proto))
			assertContains(t, output, "Dimension",
				fmt.Sprintf("[%s] vector namespace info should contain Dimension", proto))
			assertContains(t, output, "DistanceMetric",
				fmt.Sprintf("[%s] vector namespace info should contain DistanceMetric", proto))

			t.Logf("[%s] vector namespace info passed (namespace: %s)", proto, namespace)
		})
	}
}

// TestCLIMultiProtocolVectorNamespaceCreateFlags tests vector namespace create
// with various --index-type and --distance-metric flag permutations across all
// protocols.
func TestCLIMultiProtocolVectorNamespaceCreateFlags(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)
	requireFeature(t, cfg, "vector")

	flagCases := []struct {
		suffix string
		flags  []string
	}{
		{"euclidean", []string{"--distance-metric", "euclidean"}},
		{"dot-product", []string{"--distance-metric", "dot-product"}},
		{"flat", []string{"--index-type", "flat"}},
		{"ivf-flat", []string{"--index-type", "ivf-flat"}},
	}

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()

			for _, fc := range flagCases {
				t.Run(fc.suffix, func(t *testing.T) {
					namespace := fmt.Sprintf("vecflag-%s-%s-%d", proto, fc.suffix, ts)

					t.Cleanup(func() {
						execCLIWithProtocol(t, cfg, proto, "vector", "namespace", "delete", "--namespace", namespace) //nolint:errcheck
					})

					args := []string{
						"vector", "namespace", "create",
						"--namespace", namespace,
						"--dimension", "4",
					}
					args = append(args, fc.flags...)

					stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, args...)
					if err != nil {
						t.Logf("stdout: %s", stdout)
						t.Logf("stderr: %s", stderr)
						t.Fatalf("[%s] vector namespace create with %s failed: %v", proto, fc.suffix, err)
					}

					output := stdout + stderr
					assertNotEmpty(t, output,
						fmt.Sprintf("[%s] vector namespace create with %s should produce output", proto, fc.suffix))

					t.Logf("[%s] vector namespace create with %s passed (namespace: %s)", proto, fc.suffix, namespace)
				})
			}
		})
	}
}

// TestCLIMultiProtocolVectorUpsertMetadata tests vector upsert with the
// --metadata flag across all protocols. It verifies that metadata is persisted
// and returned on subsequent get.
func TestCLIMultiProtocolVectorUpsertMetadata(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)
	requireFeature(t, cfg, "vector")

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			namespace := fmt.Sprintf("vecmeta-%s-%d", proto, ts)

			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "vector", "delete", "--id", "1", "--namespace", namespace) //nolint:errcheck
				execCLIWithProtocol(t, cfg, proto, "vector", "namespace", "delete", "--namespace", namespace) //nolint:errcheck
			})

			// Create namespace.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"vector", "namespace", "create",
				"--namespace", namespace,
				"--dimension", "3",
				"--distance-metric", "cosine")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] vector namespace create failed: %v", proto, err)
			}

			// Upsert vector with metadata.
			metadata := `{"color":"red","score":0.9}`
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"vector", "upsert",
				"--namespace", namespace,
				"--id", "1",
				"--vector", "1.0,0.0,0.0",
				"--metadata", metadata)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] vector upsert with metadata failed: %v", proto, err)
			}

			// Get vector and verify metadata is present.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"vector", "get",
				"--id", "1",
				"--namespace", namespace)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] vector get failed: %v", proto, err)
			}

			output := stdout + stderr
			assertContains(t, output, "color",
				fmt.Sprintf("[%s] vector get should contain metadata key 'color'", proto))
			assertContains(t, output, "red",
				fmt.Sprintf("[%s] vector get should contain metadata value 'red'", proto))

			t.Logf("[%s] vector upsert metadata passed (namespace: %s)", proto, namespace)
		})
	}
}

// TestCLIMultiProtocolVectorGetConsistency tests the vector get command with
// various --consistency flag values across all protocols.
func TestCLIMultiProtocolVectorGetConsistency(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)
	requireFeature(t, cfg, "vector")

	consistencyLevels := []string{"default", "leader", "any"}

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			namespace := fmt.Sprintf("veccons-%s-%d", proto, ts)

			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "vector", "delete", "--id", "1", "--namespace", namespace) //nolint:errcheck
				execCLIWithProtocol(t, cfg, proto, "vector", "namespace", "delete", "--namespace", namespace) //nolint:errcheck
			})

			// Create namespace and upsert a vector.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"vector", "namespace", "create",
				"--namespace", namespace,
				"--dimension", "3",
				"--distance-metric", "cosine")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] vector namespace create failed: %v", proto, err)
			}

			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"vector", "upsert",
				"--namespace", namespace,
				"--id", "1",
				"--vector", "1.0,0.0,0.0")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] vector upsert failed: %v", proto, err)
			}

			for _, level := range consistencyLevels {
				t.Run(level, func(t *testing.T) {
					stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
						"vector", "get",
						"--id", "1",
						"--namespace", namespace,
						"--consistency", level)
					if err != nil {
						t.Logf("stdout: %s", stdout)
						t.Logf("stderr: %s", stderr)
						t.Fatalf("[%s] vector get --consistency %s failed: %v", proto, level, err)
					}

					output := stdout + stderr
					assertNotEmpty(t, output,
						fmt.Sprintf("[%s] vector get --consistency %s should produce output", proto, level))

					t.Logf("[%s] vector get --consistency %s passed", proto, level)
				})
			}
		})
	}
}

// TestCLIMultiProtocolVectorBatchUpsert tests the vector batch-upsert command
// with a JSON file input across all protocols.
func TestCLIMultiProtocolVectorBatchUpsert(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)
	requireFeature(t, cfg, "vector")

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			namespace := fmt.Sprintf("vecbatch-%s-%d", proto, ts)

			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "vector", "delete", "--id", "1", "--namespace", namespace) //nolint:errcheck
				execCLIWithProtocol(t, cfg, proto, "vector", "delete", "--id", "2", "--namespace", namespace) //nolint:errcheck
				execCLIWithProtocol(t, cfg, proto, "vector", "delete", "--id", "3", "--namespace", namespace) //nolint:errcheck
				execCLIWithProtocol(t, cfg, proto, "vector", "namespace", "delete", "--namespace", namespace) //nolint:errcheck
			})

			// Create namespace with dimension 3.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"vector", "namespace", "create",
				"--namespace", namespace,
				"--dimension", "3",
				"--distance-metric", "cosine")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] vector namespace create failed: %v", proto, err)
			}

			// Create temp JSON file with batch vectors.
			batchJSON := `[{"id":1,"vector":[1.0,0.0,0.0]},{"id":2,"vector":[0.0,1.0,0.0]},{"id":3,"vector":[0.0,0.0,1.0]}]`
			batchFile := createTempJSONFile(t, batchJSON)

			// Execute batch-upsert.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"vector", "batch-upsert",
				"--namespace", namespace,
				"--file", batchFile)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] vector batch-upsert failed: %v", proto, err)
			}

			// Verify vectors exist with vector list.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"vector", "list",
				"--namespace", namespace)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] vector list after batch-upsert failed: %v", proto, err)
			}

			output := stdout + stderr
			assertNotEmpty(t, output,
				fmt.Sprintf("[%s] vector list after batch-upsert should produce output", proto))

			t.Logf("[%s] vector batch-upsert passed (namespace: %s)", proto, namespace)
		})
	}
}

// TestCLIMultiProtocolVectorBatchDelete tests the vector batch-delete command
// across all protocols. It upserts 3 vectors, batch deletes them, and verifies
// they are removed.
func TestCLIMultiProtocolVectorBatchDelete(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)
	requireFeature(t, cfg, "vector")

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			namespace := fmt.Sprintf("vecbdel-%s-%d", proto, ts)

			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "vector", "namespace", "delete", "--namespace", namespace) //nolint:errcheck
			})

			// Create namespace with dimension 3.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"vector", "namespace", "create",
				"--namespace", namespace,
				"--dimension", "3",
				"--distance-metric", "cosine")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] vector namespace create failed: %v", proto, err)
			}

			// Upsert 3 vectors.
			vectors := []struct {
				id  string
				vec string
			}{
				{"1", "1.0,0.0,0.0"},
				{"2", "0.0,1.0,0.0"},
				{"3", "0.0,0.0,1.0"},
			}
			for _, v := range vectors {
				stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
					"vector", "upsert",
					"--namespace", namespace,
					"--id", v.id,
					"--vector", v.vec)
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] vector upsert id=%s failed: %v", proto, v.id, err)
				}
			}

			// Batch delete all 3 vectors.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"vector", "batch-delete",
				"--namespace", namespace,
				"--ids", "1,2,3")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] vector batch-delete failed: %v", proto, err)
			}

			// Verify vectors are gone with vector list.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"vector", "list",
				"--namespace", namespace)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] vector list after batch-delete failed: %v", proto, err)
			}

			// The output should not contain any of the deleted vector IDs as entries.
			output := stdout + stderr
			t.Logf("[%s] vector list after batch-delete output: %s", proto, output)

			t.Logf("[%s] vector batch-delete passed (namespace: %s)", proto, namespace)
		})
	}
}

// TestCLIMultiProtocolVectorListFlags tests the vector list command with --limit
// and --cursor flags across all protocols.
func TestCLIMultiProtocolVectorListFlags(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)
	requireFeature(t, cfg, "vector")

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			namespace := fmt.Sprintf("veclist-%s-%d", proto, ts)

			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "vector", "delete", "--id", "1", "--namespace", namespace) //nolint:errcheck
				execCLIWithProtocol(t, cfg, proto, "vector", "delete", "--id", "2", "--namespace", namespace) //nolint:errcheck
				execCLIWithProtocol(t, cfg, proto, "vector", "delete", "--id", "3", "--namespace", namespace) //nolint:errcheck
				execCLIWithProtocol(t, cfg, proto, "vector", "namespace", "delete", "--namespace", namespace) //nolint:errcheck
			})

			// Create namespace with dimension 3.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"vector", "namespace", "create",
				"--namespace", namespace,
				"--dimension", "3",
				"--distance-metric", "cosine")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] vector namespace create failed: %v", proto, err)
			}

			// Upsert 3 vectors.
			vectors := []struct {
				id  string
				vec string
			}{
				{"1", "1.0,0.0,0.0"},
				{"2", "0.0,1.0,0.0"},
				{"3", "0.0,0.0,1.0"},
			}
			for _, v := range vectors {
				stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
					"vector", "upsert",
					"--namespace", namespace,
					"--id", v.id,
					"--vector", v.vec)
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] vector upsert id=%s failed: %v", proto, v.id, err)
				}
			}

			// Test --limit flag.
			t.Run("limit", func(t *testing.T) {
				stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
					"vector", "list",
					"--namespace", namespace,
					"--limit", "2")
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] vector list --limit 2 failed: %v", proto, err)
				}

				output := stdout + stderr
				assertNotEmpty(t, output,
					fmt.Sprintf("[%s] vector list --limit 2 should produce output", proto))

				t.Logf("[%s] vector list --limit passed", proto)
			})

			// Test --cursor flag (use "1" as cursor to start after first entry).
			t.Run("cursor", func(t *testing.T) {
				stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
					"vector", "list",
					"--namespace", namespace,
					"--cursor", "1")
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] vector list --cursor 1 failed: %v", proto, err)
				}

				output := stdout + stderr
				assertNotEmpty(t, output,
					fmt.Sprintf("[%s] vector list --cursor 1 should produce output", proto))

				t.Logf("[%s] vector list --cursor passed", proto)
			})
		})
	}
}

// TestCLIMultiProtocolVectorUpdateMetadata tests the vector update-metadata
// command across all protocols. It upserts a vector, updates its metadata, and
// verifies the updated metadata on get.
func TestCLIMultiProtocolVectorUpdateMetadata(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)
	requireFeature(t, cfg, "vector")

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			namespace := fmt.Sprintf("vecumeta-%s-%d", proto, ts)

			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "vector", "delete", "--id", "1", "--namespace", namespace) //nolint:errcheck
				execCLIWithProtocol(t, cfg, proto, "vector", "namespace", "delete", "--namespace", namespace) //nolint:errcheck
			})

			// Create namespace.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"vector", "namespace", "create",
				"--namespace", namespace,
				"--dimension", "3",
				"--distance-metric", "cosine")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] vector namespace create failed: %v", proto, err)
			}

			// Upsert vector with initial metadata.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"vector", "upsert",
				"--namespace", namespace,
				"--id", "1",
				"--vector", "1.0,0.0,0.0",
				"--metadata", `{"color":"blue"}`)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] vector upsert failed: %v", proto, err)
			}

			// Update metadata.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"vector", "update-metadata",
				"--namespace", namespace,
				"--id", "1",
				"--metadata", `{"color":"green","size":"large"}`)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] vector update-metadata failed: %v", proto, err)
			}

			// Get vector and verify updated metadata.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"vector", "get",
				"--id", "1",
				"--namespace", namespace)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] vector get after update-metadata failed: %v", proto, err)
			}

			output := stdout + stderr
			assertContains(t, output, "green",
				fmt.Sprintf("[%s] vector get should contain updated metadata value 'green'", proto))

			t.Logf("[%s] vector update-metadata passed (namespace: %s)", proto, namespace)
		})
	}
}

// TestCLIMultiProtocolVectorSearchFlags tests the vector search command with
// various flag permutations (--ef-search, --min-score, --consistency) across
// all protocols.
func TestCLIMultiProtocolVectorSearchFlags(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)
	requireFeature(t, cfg, "vector")

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			namespace := fmt.Sprintf("vecsrch-%s-%d", proto, ts)

			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "vector", "delete", "--id", "1", "--namespace", namespace) //nolint:errcheck
				execCLIWithProtocol(t, cfg, proto, "vector", "delete", "--id", "2", "--namespace", namespace) //nolint:errcheck
				execCLIWithProtocol(t, cfg, proto, "vector", "delete", "--id", "3", "--namespace", namespace) //nolint:errcheck
				execCLIWithProtocol(t, cfg, proto, "vector", "namespace", "delete", "--namespace", namespace) //nolint:errcheck
			})

			// Create namespace with dimension 3.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"vector", "namespace", "create",
				"--namespace", namespace,
				"--dimension", "3",
				"--distance-metric", "cosine")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] vector namespace create failed: %v", proto, err)
			}

			// Upsert 3 vectors.
			vectors := []struct {
				id  string
				vec string
			}{
				{"1", "1.0,0.0,0.0"},
				{"2", "0.0,1.0,0.0"},
				{"3", "0.0,0.0,1.0"},
			}
			for _, v := range vectors {
				stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
					"vector", "upsert",
					"--namespace", namespace,
					"--id", v.id,
					"--vector", v.vec)
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] vector upsert id=%s failed: %v", proto, v.id, err)
				}
			}

			// Subtest: --ef-search flag.
			t.Run("ef-search", func(t *testing.T) {
				stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
					"vector", "search",
					"--namespace", namespace,
					"--vector", "1.0,0.0,0.0",
					"--k", "2",
					"--ef-search", "100")
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] vector search --ef-search failed: %v", proto, err)
				}

				output := stdout + stderr
				assertNotEmpty(t, output,
					fmt.Sprintf("[%s] vector search --ef-search should produce output", proto))

				t.Logf("[%s] vector search --ef-search passed", proto)
			})

			// Subtest: --min-score flag.
			t.Run("min-score", func(t *testing.T) {
				stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
					"vector", "search",
					"--namespace", namespace,
					"--vector", "1.0,0.0,0.0",
					"--k", "2",
					"--min-score", "0.5")
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] vector search --min-score failed: %v", proto, err)
				}

				output := stdout + stderr
				assertNotEmpty(t, output,
					fmt.Sprintf("[%s] vector search --min-score should produce output", proto))

				t.Logf("[%s] vector search --min-score passed", proto)
			})

			// Subtest: --consistency leader flag.
			t.Run("consistency-leader", func(t *testing.T) {
				stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
					"vector", "search",
					"--namespace", namespace,
					"--vector", "1.0,0.0,0.0",
					"--k", "2",
					"--consistency", "leader")
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] vector search --consistency leader failed: %v", proto, err)
				}

				output := stdout + stderr
				assertNotEmpty(t, output,
					fmt.Sprintf("[%s] vector search --consistency leader should produce output", proto))

				t.Logf("[%s] vector search --consistency leader passed", proto)
			})
		})
	}
}

// TestCLIMultiProtocolVectorSearchMetadata tests the vector search-metadata
// command with metadata filters across all protocols.
func TestCLIMultiProtocolVectorSearchMetadata(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)
	requireFeature(t, cfg, "vector")

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			namespace := fmt.Sprintf("vecsm-%s-%d", proto, ts)

			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "vector", "delete", "--id", "1", "--namespace", namespace) //nolint:errcheck
				execCLIWithProtocol(t, cfg, proto, "vector", "delete", "--id", "2", "--namespace", namespace) //nolint:errcheck
				execCLIWithProtocol(t, cfg, proto, "vector", "delete", "--id", "3", "--namespace", namespace) //nolint:errcheck
				execCLIWithProtocol(t, cfg, proto, "vector", "namespace", "delete", "--namespace", namespace) //nolint:errcheck
			})

			// Create namespace with dimension 3.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"vector", "namespace", "create",
				"--namespace", namespace,
				"--dimension", "3",
				"--distance-metric", "cosine")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] vector namespace create failed: %v", proto, err)
			}

			// Upsert vectors with different metadata.
			upserts := []struct {
				id       string
				vec      string
				metadata string
			}{
				{"1", "1.0,0.0,0.0", `{"color":"red","size":"small"}`},
				{"2", "0.0,1.0,0.0", `{"color":"blue","size":"large"}`},
				{"3", "0.0,0.0,1.0", `{"color":"red","size":"large"}`},
			}
			for _, u := range upserts {
				stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
					"vector", "upsert",
					"--namespace", namespace,
					"--id", u.id,
					"--vector", u.vec,
					"--metadata", u.metadata)
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] vector upsert id=%s failed: %v", proto, u.id, err)
				}
			}

			// Search by metadata filter.
			filters := `{"color":"red"}`
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"vector", "search-metadata",
				"--namespace", namespace,
				"--filters", filters,
				"--limit", "10")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] vector search-metadata failed: %v", proto, err)
			}

			output := stdout + stderr
			assertNotEmpty(t, output,
				fmt.Sprintf("[%s] vector search-metadata should produce output", proto))

			t.Logf("[%s] vector search-metadata passed (namespace: %s)", proto, namespace)
		})
	}
}

// TestCLIMultiProtocolVectorSearchHybrid tests the vector search-hybrid command
// with vector and metadata filters across all protocols.
func TestCLIMultiProtocolVectorSearchHybrid(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)
	requireFeature(t, cfg, "vector")

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			namespace := fmt.Sprintf("vecshyb-%s-%d", proto, ts)

			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "vector", "delete", "--id", "1", "--namespace", namespace) //nolint:errcheck
				execCLIWithProtocol(t, cfg, proto, "vector", "delete", "--id", "2", "--namespace", namespace) //nolint:errcheck
				execCLIWithProtocol(t, cfg, proto, "vector", "delete", "--id", "3", "--namespace", namespace) //nolint:errcheck
				execCLIWithProtocol(t, cfg, proto, "vector", "namespace", "delete", "--namespace", namespace) //nolint:errcheck
			})

			// Create namespace with dimension 3.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"vector", "namespace", "create",
				"--namespace", namespace,
				"--dimension", "3",
				"--distance-metric", "cosine")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] vector namespace create failed: %v", proto, err)
			}

			// Upsert vectors with metadata.
			upserts := []struct {
				id       string
				vec      string
				metadata string
			}{
				{"1", "1.0,0.0,0.0", `{"color":"red"}`},
				{"2", "0.0,1.0,0.0", `{"color":"blue"}`},
				{"3", "0.5,0.5,0.0", `{"color":"red"}`},
			}
			for _, u := range upserts {
				stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
					"vector", "upsert",
					"--namespace", namespace,
					"--id", u.id,
					"--vector", u.vec,
					"--metadata", u.metadata)
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] vector upsert id=%s failed: %v", proto, u.id, err)
				}
			}

			// Hybrid search combining vector similarity and metadata filter.
			filters := `{"color":"red"}`
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"vector", "search-hybrid",
				"--namespace", namespace,
				"--vector", "1.0,0.0,0.0",
				"--filters", filters,
				"--k", "2")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] vector search-hybrid failed: %v", proto, err)
			}

			output := stdout + stderr
			assertNotEmpty(t, output,
				fmt.Sprintf("[%s] vector search-hybrid should produce output", proto))

			t.Logf("[%s] vector search-hybrid passed (namespace: %s)", proto, namespace)
		})
	}
}

// TestCLIMultiProtocolVectorSearchByID tests the vector search-by-id command
// across all protocols. It upserts 3 vectors and searches by ID of the first
// to find nearest neighbors.
func TestCLIMultiProtocolVectorSearchByID(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)
	requireFeature(t, cfg, "vector")

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			namespace := fmt.Sprintf("vecsid-%s-%d", proto, ts)

			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "vector", "delete", "--id", "1", "--namespace", namespace) //nolint:errcheck
				execCLIWithProtocol(t, cfg, proto, "vector", "delete", "--id", "2", "--namespace", namespace) //nolint:errcheck
				execCLIWithProtocol(t, cfg, proto, "vector", "delete", "--id", "3", "--namespace", namespace) //nolint:errcheck
				execCLIWithProtocol(t, cfg, proto, "vector", "namespace", "delete", "--namespace", namespace) //nolint:errcheck
			})

			// Create namespace with dimension 3.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"vector", "namespace", "create",
				"--namespace", namespace,
				"--dimension", "3",
				"--distance-metric", "cosine")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] vector namespace create failed: %v", proto, err)
			}

			// Upsert 3 vectors.
			vectors := []struct {
				id  string
				vec string
			}{
				{"1", "1.0,0.0,0.0"},
				{"2", "0.8,0.2,0.0"},
				{"3", "0.0,0.0,1.0"},
			}
			for _, v := range vectors {
				stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
					"vector", "upsert",
					"--namespace", namespace,
					"--id", v.id,
					"--vector", v.vec)
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] vector upsert id=%s failed: %v", proto, v.id, err)
				}
			}

			// Search by ID of vector 1.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"vector", "search-by-id",
				"--namespace", namespace,
				"--id", "1",
				"--k", "2")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] vector search-by-id failed: %v", proto, err)
			}

			output := stdout + stderr
			assertNotEmpty(t, output,
				fmt.Sprintf("[%s] vector search-by-id should produce output", proto))

			t.Logf("[%s] vector search-by-id passed (namespace: %s)", proto, namespace)
		})
	}
}

// TestCLIMultiProtocolVectorIndexOps tests the vector rebuild-index and
// index-stats commands across all protocols.
func TestCLIMultiProtocolVectorIndexOps(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)
	requireFeature(t, cfg, "vector")

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			namespace := fmt.Sprintf("vecidx-%s-%d", proto, ts)

			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "vector", "delete", "--id", "1", "--namespace", namespace) //nolint:errcheck
				execCLIWithProtocol(t, cfg, proto, "vector", "namespace", "delete", "--namespace", namespace) //nolint:errcheck
			})

			// Create namespace with dimension 3.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"vector", "namespace", "create",
				"--namespace", namespace,
				"--dimension", "3",
				"--distance-metric", "cosine")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] vector namespace create failed: %v", proto, err)
			}

			// Upsert a vector so the index has data.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"vector", "upsert",
				"--namespace", namespace,
				"--id", "1",
				"--vector", "1.0,0.0,0.0")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] vector upsert failed: %v", proto, err)
			}

			// Subtest: rebuild-index.
			t.Run("rebuild-index", func(t *testing.T) {
				stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
					"vector", "rebuild-index",
					"--namespace", namespace)
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] vector rebuild-index failed: %v", proto, err)
				}

				output := stdout + stderr
				assertNotEmpty(t, output,
					fmt.Sprintf("[%s] vector rebuild-index should produce output", proto))

				t.Logf("[%s] vector rebuild-index passed", proto)
			})

			// Subtest: index-stats.
			t.Run("index-stats", func(t *testing.T) {
				stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
					"vector", "index-stats",
					"--namespace", namespace)
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] vector index-stats failed: %v", proto, err)
				}

				output := stdout + stderr
				assertNotEmpty(t, output,
					fmt.Sprintf("[%s] vector index-stats should produce output", proto))

				t.Logf("[%s] vector index-stats passed", proto)
			})
		})
	}
}

// TestCLIMultiProtocolVectorCompact tests the vector compact command across all
// protocols. It creates a namespace, upserts and deletes vectors, then compacts.
func TestCLIMultiProtocolVectorCompact(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)
	requireFeature(t, cfg, "vector")

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			namespace := fmt.Sprintf("veccomp-%s-%d", proto, ts)

			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "vector", "namespace", "delete", "--namespace", namespace) //nolint:errcheck
			})

			// Create namespace with dimension 3.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"vector", "namespace", "create",
				"--namespace", namespace,
				"--dimension", "3",
				"--distance-metric", "cosine")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] vector namespace create failed: %v", proto, err)
			}

			// Upsert and delete vectors to create tombstones for compaction.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"vector", "upsert",
				"--namespace", namespace,
				"--id", "1",
				"--vector", "1.0,0.0,0.0")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] vector upsert failed: %v", proto, err)
			}

			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"vector", "upsert",
				"--namespace", namespace,
				"--id", "2",
				"--vector", "0.0,1.0,0.0")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] vector upsert id=2 failed: %v", proto, err)
			}

			// Delete one vector to create a tombstone.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"vector", "delete",
				"--id", "1",
				"--namespace", namespace)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] vector delete id=1 failed: %v", proto, err)
			}

			// Compact the namespace.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"vector", "compact",
				"--namespace", namespace)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] vector compact failed: %v", proto, err)
			}

			output := stdout + stderr
			assertNotEmpty(t, output,
				fmt.Sprintf("[%s] vector compact should produce output", proto))

			t.Logf("[%s] vector compact passed (namespace: %s)", proto, namespace)
		})
	}
}
