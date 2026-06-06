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
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/jeremyhahn/go-quicraft/test/performance/perfresult"
)

// SystemRunner holds configuration for running a benchmark system via Docker.
type SystemRunner struct {
	System         string
	Image          string
	Dockerfile     string
	BuildCtx       string
	ExtraBuildArgs []string // Extra args passed to docker build (e.g. --build-context).
	DockerRunArgs  []string // Extra docker run args (before image name).
	BenchPattern   string   // Benchmark filter pattern (default ".").
	PreRun         func() error
	PostRun        func()
}

// RunnerFactory creates a SystemRunner with the correct paths for the given
// repository roots. baseDir is the go-quicraft root (for etcd consensus
// benchmarks); qrdbRoot is the go-qrdb root (for QRDB, TiKV, and etcd-API
// benchmarks whose Dockerfiles and compose files live in go-qrdb).
type RunnerFactory func(baseDir, qrdbRoot string) *SystemRunner

// runners is the map-based dispatch table for KV benchmark system factories.
var runners = map[string]RunnerFactory{
	"qrdb-pebble-h2": newQRDBPebbleH2Runner,
	"qrdb-pebble-h3": newQRDBPebbleH3Runner,
	"qrdb-memory-h2": newQRDBMemoryH2Runner,
	"qrdb-grpc":      newQRDBGRPCRunner,
	"qrdb-quic":      newQRDBQUICRunner,
	"qrdb-etcd":      newQRDBEtcdRunner,
	"etcd":           newEtcdRunner,
	"tikv":           newTiKVRunner,
	"tikv-3n":        newTiKV3NRunner,
}

// ErrDockerBuild indicates that the Docker image build step failed.
type ErrDockerBuild struct {
	System string
	Detail string
}

func (e *ErrDockerBuild) Error() string {
	return "docker build failed for " + e.System + ": " + e.Detail
}

// ErrDockerRun indicates that the Docker container execution failed.
type ErrDockerRun struct {
	System string
	Detail string
}

func (e *ErrDockerRun) Error() string {
	return "docker run failed for " + e.System + ": " + e.Detail
}

// ErrDockerNotFound indicates that the docker CLI is not available on PATH.
type ErrDockerNotFound struct{}

func (e *ErrDockerNotFound) Error() string {
	return "docker command not found; ensure Docker is installed and on PATH"
}

// ErrPreRunFailed indicates that the pre-run hook failed.
type ErrPreRunFailed struct {
	System string
	Detail string
}

func (e *ErrPreRunFailed) Error() string {
	return "pre-run hook failed for " + e.System + ": " + e.Detail
}

// Run builds the Docker image (unless noBuild), runs the container,
// captures stdout, and extracts the BenchmarkSuite JSON from output.
func (sr *SystemRunner) Run(benchtime, timeout string, noBuild bool) (*perfresult.BenchmarkSuite, error) {
	if _, err := exec.LookPath("docker"); err != nil {
		return nil, &ErrDockerNotFound{}
	}

	// Pre-run hook (e.g., start TiKV cluster).
	if sr.PreRun != nil {
		if err := sr.PreRun(); err != nil {
			return nil, &ErrPreRunFailed{System: sr.System, Detail: err.Error()}
		}
	}

	// Post-run cleanup (always runs, even on error).
	if sr.PostRun != nil {
		defer sr.PostRun()
	}

	// Docker build.
	if !noBuild {
		if err := sr.dockerBuild(); err != nil {
			return nil, err
		}
	}

	// Docker run.
	output, runErr := sr.dockerRun(benchtime, timeout)

	// Attempt JSON extraction even on non-zero exit: some benchmark
	// binaries produce valid results but return a non-zero exit code.
	if len(output) > 0 {
		suite, parseErr := perfresult.ReadSuiteFromOutput(output)
		if parseErr == nil {
			if runErr != nil {
				fmt.Fprintf(os.Stderr, "  WARNING: %s exited non-zero but produced valid results\n", sr.System)
			}
			return suite, nil
		}
		// JSON extraction failed -- fall through to return the run error.
	}

	if runErr != nil {
		return nil, runErr
	}

	// No output and no error -- should not happen, but handle gracefully.
	return nil, &ErrDockerRun{System: sr.System, Detail: "no output produced"}
}

// dockerBuild executes `docker build` for the system's Dockerfile.
func (sr *SystemRunner) dockerBuild() error {
	args := []string{"build"}
	args = append(args, sr.ExtraBuildArgs...)
	args = append(args,
		"-t", sr.Image,
		"-f", sr.Dockerfile,
		sr.BuildCtx,
	)

	fmt.Fprintf(os.Stderr, "  Building image %s ...\n", sr.Image)

	cmd := exec.Command("docker", args...)
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return &ErrDockerBuild{System: sr.System, Detail: err.Error()}
	}

	return nil
}

// dockerRun executes `docker run --rm` and captures stdout.
// The benchtime and timeout flags override the CMD in the Dockerfile.
func (sr *SystemRunner) dockerRun(benchtime, timeout string) ([]byte, error) {
	pattern := sr.BenchPattern
	if pattern == "" {
		pattern = "."
	}

	args := []string{"run", "--rm"}
	args = append(args, sr.DockerRunArgs...)
	args = append(args,
		sr.Image,
		"-test.bench="+pattern,
		"-test.benchtime="+benchtime,
		"-test.timeout="+timeout,
		"-test.benchmem",
		"-test.count=1",
		"-test.v",
	)

	fmt.Fprintf(os.Stderr, "  Running container %s ...\n", sr.Image)

	cmd := exec.Command("docker", args...)
	cmd.Stderr = os.Stderr

	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	runErr := cmd.Run()

	// Return stdout even on non-zero exit: some benchmark binaries
	// (e.g., Go test -bench) exit 1 despite producing valid JSON results.
	// The caller attempts JSON extraction and falls back to the error.
	if stdout.Len() > 0 {
		return stdout.Bytes(), runErr
	}

	if runErr != nil {
		return nil, &ErrDockerRun{System: sr.System, Detail: runErr.Error()}
	}

	return stdout.Bytes(), nil
}

// quicraftBuildContext returns the --build-context flag for go-quicraft when
// baseDir is non-empty. The Dockerfiles mount go-quicraft source at the named
// context "go-quicraft" (accessible as /go-quicraft inside the build).
func quicraftBuildContext(baseDir string) []string {
	if baseDir == "" {
		return nil
	}
	return []string{"--build-context", "go-quicraft=" + baseDir}
}

// newQRDBPebbleH2Runner creates a SystemRunner for QRDB with PebbleDB+HTTP/2.
func newQRDBPebbleH2Runner(baseDir, qrdbRoot string) *SystemRunner {
	return &SystemRunner{
		System:         "qrdb-pebble-h2",
		Image:          "qrdb-kvbench-qrdb",
		Dockerfile:     filepath.Join(qrdbRoot, "test/performance/qrdb/Dockerfile"),
		BuildCtx:       qrdbRoot,
		ExtraBuildArgs: quicraftBuildContext(baseDir),
		DockerRunArgs: []string{
			"--env", "QRDB_BENCH_SM=pebble",
			"--env", "QRDB_BENCH_PROTOCOL=h2",
		},
	}
}

// newQRDBPebbleH3Runner creates a SystemRunner for QRDB with PebbleDB+HTTP/3.
func newQRDBPebbleH3Runner(baseDir, qrdbRoot string) *SystemRunner {
	return &SystemRunner{
		System:         "qrdb-pebble-h3",
		Image:          "qrdb-kvbench-qrdb",
		Dockerfile:     filepath.Join(qrdbRoot, "test/performance/qrdb/Dockerfile"),
		BuildCtx:       qrdbRoot,
		ExtraBuildArgs: quicraftBuildContext(baseDir),
		DockerRunArgs: []string{
			"--env", "QRDB_BENCH_SM=pebble",
			"--env", "QRDB_BENCH_PROTOCOL=h3",
		},
	}
}

// newQRDBMemoryH2Runner creates a SystemRunner for QRDB with in-memory SM+HTTP/2.
func newQRDBMemoryH2Runner(baseDir, qrdbRoot string) *SystemRunner {
	return &SystemRunner{
		System:         "qrdb-memory-h2",
		Image:          "qrdb-kvbench-qrdb",
		Dockerfile:     filepath.Join(qrdbRoot, "test/performance/qrdb/Dockerfile"),
		BuildCtx:       qrdbRoot,
		ExtraBuildArgs: quicraftBuildContext(baseDir),
		DockerRunArgs: []string{
			"--env", "QRDB_BENCH_SM=memory",
			"--env", "QRDB_BENCH_PROTOCOL=h2",
		},
	}
}

// newQRDBGRPCRunner creates a SystemRunner for QRDB using the gRPC/protobuf
// protocol with PebbleDB storage. Only the GRPC-prefixed benchmarks are run
// to isolate gRPC protocol overhead from the HTTP-based benchmarks.
func newQRDBGRPCRunner(baseDir, qrdbRoot string) *SystemRunner {
	return &SystemRunner{
		System:         "qrdb-grpc",
		Image:          "qrdb-kvbench-qrdb",
		Dockerfile:     filepath.Join(qrdbRoot, "test/performance/qrdb/Dockerfile"),
		BuildCtx:       qrdbRoot,
		ExtraBuildArgs: quicraftBuildContext(baseDir),
		BenchPattern:   "BenchmarkQRDB_GRPC",
		DockerRunArgs: []string{
			"--env", "QRDB_BENCH_SM=pebble",
			"--env", "QRDB_BENCH_PROTOCOL=grpc",
		},
	}
}

// newQRDBQUICRunner creates a SystemRunner for QRDB using the native QUIC
// wire protocol with PebbleDB storage. Only the QUIC-prefixed benchmarks are
// run to isolate native QUIC protocol characteristics.
func newQRDBQUICRunner(baseDir, qrdbRoot string) *SystemRunner {
	return &SystemRunner{
		System:         "qrdb-quic",
		Image:          "qrdb-kvbench-qrdb",
		Dockerfile:     filepath.Join(qrdbRoot, "test/performance/qrdb/Dockerfile"),
		BuildCtx:       qrdbRoot,
		ExtraBuildArgs: quicraftBuildContext(baseDir),
		BenchPattern:   "BenchmarkQRDB_QUIC",
		DockerRunArgs: []string{
			"--env", "QRDB_BENCH_SM=pebble",
			"--env", "QRDB_BENCH_PROTOCOL=quic",
		},
	}
}

// newTiKVRunner creates a SystemRunner for TiKV KV benchmarks.
// PreRun starts TiKV via docker compose; PostRun tears it down.
func newTiKVRunner(baseDir, qrdbRoot string) *SystemRunner {
	composeFile := filepath.Join(qrdbRoot, "test/performance/tikv/docker-compose.yml")

	return &SystemRunner{
		System:         "tikv",
		Image:          "qrdb-kvbench-tikv",
		Dockerfile:     filepath.Join(qrdbRoot, "test/performance/tikv/Dockerfile"),
		BuildCtx:       qrdbRoot,
		ExtraBuildArgs: quicraftBuildContext(baseDir),
		DockerRunArgs: []string{
			"--network=host",
			"-v", "/var/run/docker.sock:/var/run/docker.sock",
			"-v", "tikv_certs:/certs:ro",
		},
		PreRun: func() error {
			fmt.Fprintf(os.Stderr, "  Starting TiKV cluster via docker compose ...\n")
			cmd := exec.Command("docker", "compose", "-f", composeFile, "up", "-d", "--wait")
			cmd.Stderr = os.Stderr
			return cmd.Run()
		},
		PostRun: func() {
			fmt.Fprintf(os.Stderr, "  Stopping TiKV cluster ...\n")
			cmd := exec.Command("docker", "compose", "-f", composeFile, "down", "-v")
			cmd.Stderr = os.Stderr
			cmd.Run()
		},
	}
}

// newQRDBEtcdRunner creates a SystemRunner for QRDB accessed via the etcd API.
// The benchmark binary starts QRDB internally (ETCD_BENCH_MODE=qrdb).
func newQRDBEtcdRunner(baseDir, qrdbRoot string) *SystemRunner {
	return &SystemRunner{
		System:         "qrdb-etcd",
		Image:          "qrdb-kvbench-etcd",
		Dockerfile:     filepath.Join(qrdbRoot, "test/performance/etcd-benchmark/Dockerfile"),
		BuildCtx:       qrdbRoot,
		ExtraBuildArgs: quicraftBuildContext(baseDir),
		DockerRunArgs: []string{
			"--env", "ETCD_BENCH_SYSTEM=qrdb-etcd",
			"--env", "ETCD_BENCH_MODE=qrdb",
		},
	}
}

// newEtcdRunner creates a SystemRunner for a standalone etcd server.
// PreRun starts etcd via docker compose; PostRun tears it down.
func newEtcdRunner(baseDir, qrdbRoot string) *SystemRunner {
	composeFile := filepath.Join(qrdbRoot, "test/performance/etcd-benchmark/docker-compose-etcd.yml")

	// The docker compose project name defaults to the compose file's parent
	// directory name ("etcd-benchmark"), which prefixes all named volumes.
	// The benchmark container must mount the same prefixed volume name.
	const composeVolume = "etcd-benchmark_etcd_bench_certs"

	return &SystemRunner{
		System:         "etcd",
		Image:          "qrdb-kvbench-etcd",
		Dockerfile:     filepath.Join(qrdbRoot, "test/performance/etcd-benchmark/Dockerfile"),
		BuildCtx:       qrdbRoot,
		ExtraBuildArgs: quicraftBuildContext(baseDir),
		DockerRunArgs: []string{
			"--network=host",
			"-v", composeVolume + ":/certs:ro",
			"--env", "ETCD_BENCH_SYSTEM=etcd",
			"--env", "ETCD_BENCH_MODE=external",
			"--env", "ETCD_ENDPOINT=127.0.0.1:2379",
		},
		PreRun: func() error {
			// Force-remove any leftover containers and volumes from a prior
			// failed run before attempting to start. This prevents port
			// conflicts and stale cert volumes from causing hangs.
			cleanup := exec.Command("docker", "compose", "-f", composeFile, "down", "-v")
			cleanup.Stderr = os.Stderr
			cleanup.Run() // best-effort, ignore errors

			fmt.Fprintf(os.Stderr, "  Starting etcd server via docker compose ...\n")
			cmd := exec.Command("docker", "compose", "-f", composeFile, "up", "-d", "--wait", "--wait-timeout", "120")
			cmd.Stderr = os.Stderr
			return cmd.Run()
		},
		PostRun: func() {
			fmt.Fprintf(os.Stderr, "  Stopping etcd server ...\n")
			cmd := exec.Command("docker", "compose", "-f", composeFile, "down", "-v")
			cmd.Stderr = os.Stderr
			cmd.Run()
		},
	}
}

// newTiKV3NRunner creates a SystemRunner for TiKV KV benchmarks against a
// 3-store cluster. Only Put and Get scenarios are run to match QRDB's 3N set.
// Uses a separate compose project name ("tikv3n") to avoid volume conflicts
// with the 1N runner.
func newTiKV3NRunner(baseDir, qrdbRoot string) *SystemRunner {
	composeFile := filepath.Join(qrdbRoot, "test/performance/tikv/docker-compose-3n.yml")

	return &SystemRunner{
		System:         "tikv-3n",
		Image:          "qrdb-kvbench-tikv",
		Dockerfile:     filepath.Join(qrdbRoot, "test/performance/tikv/Dockerfile"),
		BuildCtx:       qrdbRoot,
		ExtraBuildArgs: quicraftBuildContext(baseDir),
		BenchPattern:   "BenchmarkTiKV_KV(Put|Get)",
		DockerRunArgs: []string{
			"--network=host",
			"-v", "/var/run/docker.sock:/var/run/docker.sock",
			"-v", "tikv3n_certs:/certs:ro",
			"--env", "TIKV_CLUSTER_NODES=3",
		},
		PreRun: func() error {
			fmt.Fprintf(os.Stderr, "  Starting TiKV 3N cluster via docker compose ...\n")
			cmd := exec.Command("docker", "compose", "-f", composeFile, "-p", "tikv3n", "up", "-d", "--wait")
			cmd.Stderr = os.Stderr
			return cmd.Run()
		},
		PostRun: func() {
			fmt.Fprintf(os.Stderr, "  Stopping TiKV 3N cluster ...\n")
			cmd := exec.Command("docker", "compose", "-f", composeFile, "-p", "tikv3n", "down", "-v")
			cmd.Stderr = os.Stderr
			cmd.Run()
		},
	}
}
