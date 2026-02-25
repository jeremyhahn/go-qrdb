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
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/store"
	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/jeremyhahn/go-quicraft/pkg/bootstrap"
	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"
	"github.com/jeremyhahn/go-quicraft/pkg/discovery"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"
	"github.com/spf13/cobra"
)

// startFlags holds the parsed CLI flags for the start command.
type startFlags struct {
	configFile           string
	listen               string
	raftAddress          string
	dataDir              string
	deploymentID         uint64
	smType               string
	discoveryMethod      string
	peers                []string
	shardID              uint64
	replicaID            uint64
	multicastSecret      string
	dnsDomain            string
	adminPort            int
	grpcPort             int
	maxInMemLogSize      uint64
	maxTotalInMemLogSize uint64
	snapshotEntries      uint64
	compactionOverhead   uint64
	join                 bool
	logFormat            string
	logLevel             string
	debugRaft            bool
	enableREST           bool
	enableGRPC           bool
	tlsCAFile            string
	tlsCertFile          string
	tlsKeyFile           string
}

// raftNode is the interface that the start command requires for shard
// lifecycle operations. Both *quicraft.Host and test mocks satisfy it.
type raftNode interface {
	StartShard(members map[uint64]string, join bool, create sm.CreateFunc, cfg quicraftconfig.Config) error
	GetLeaderID(shardID uint64) (uint64, bool, error)
	Close() error
}

// smFactories maps state machine type names to their factory functions.
// O(1) dispatch avoids a switch statement.
var smFactories = map[string]func() sm.CreateFunc{
	"memory":     kv.NewMemoryCreateFunc,
	"concurrent": kv.NewConcurrentCreateFunc,
	"pebble":     func() sm.CreateFunc { return store.NewPebbleCreateFunc() },
}

// discoveryBuilder is a function that builds a discovery.Method from the
// start flags. Each discovery method has its own builder.
type discoveryBuilder func(f *startFlags) (discovery.Method, error)

// discoveryBuilders maps discovery method names to their builder functions.
var discoveryBuilders = map[string]discoveryBuilder{
	"static":    buildStaticDiscovery,
	"multicast": buildMulticastDiscovery,
	"dns":       buildDNSDiscovery,
}

func newStartCmd() *cobra.Command {
	f := &startFlags{}

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start a QuicRaft DB node",
		Long: "Start a QuicRaft DB node. Supports two configuration modes:\n" +
			"  1. YAML config file via --config flag\n" +
			"  2. CLI flags (backward compatible)\n" +
			"CLI flags override YAML values when both are provided.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Load YAML config if --config is provided, then let CLI
			// flags override any YAML values that were explicitly set.
			if f.configFile != "" {
				cfg, err := qrdbconfig.Load(f.configFile)
				if err != nil {
					return err
				}
				applyConfigToFlags(cmd, f, cfg)
			}

			// Configure the global logger early so all pre-host log
			// lines (auto-generated replica ID, etc.) use the
			// requested format and level.
			logCfg := quicraftconfig.LogConfig{
				Format: quicraftconfig.LogFormat(f.logFormat),
				Level:  quicraftconfig.LogLevel(f.logLevel),
			}
			logCfg.SetDefaults()
			if f.debugRaft {
				logCfg.Level = quicraftconfig.LogLevelDebug
			}
			if err := logCfg.Validate(); err != nil {
				return err
			}
			quicraftconfig.SetDefaultLogger(logCfg)

			if err := resolveAutoConfig(cmd, f); err != nil {
				return err
			}
			return runStart(f)
		},
	}

	cmd.Flags().StringVar(&f.configFile, "config", "", "Path to YAML configuration file")
	cmd.Flags().StringVar(&f.listen, "listen", "0.0.0.0:63001", "Listen address (host:port)")
	cmd.Flags().StringVar(&f.raftAddress, "raft-address", "127.0.0.1:63001", "Raft address visible to peers")
	cmd.Flags().StringVar(&f.dataDir, "data-dir", "./data", "Data directory for WAL and metadata")
	cmd.Flags().Uint64Var(&f.deploymentID, "deployment-id", 1, "Deployment ID for cluster isolation")
	cmd.Flags().StringVar(&f.smType, "sm", "pebble", "State machine type: pebble, memory, concurrent")
	cmd.Flags().StringVar(&f.discoveryMethod, "discovery", "multicast", "Discovery method: multicast, static, dns")
	cmd.Flags().StringSliceVar(&f.peers, "peers", nil, "Static peers, format id=address (e.g., 1=127.0.0.1:63001)")
	cmd.Flags().Uint64Var(&f.shardID, "shard-id", 1, "Shard ID")
	cmd.Flags().Uint64Var(&f.replicaID, "replica-id", 1, "Replica ID")
	cmd.Flags().StringVar(&f.multicastSecret, "multicast-secret", "quicraft", "Shared secret for multicast authentication")
	cmd.Flags().StringVar(&f.dnsDomain, "dns-domain", "", "DNS SRV domain for DNS discovery")
	cmd.Flags().IntVar(&f.adminPort, "admin-port", 63002, "Admin HTTP server port")
	cmd.Flags().IntVar(&f.grpcPort, "grpc-port", 63003, "gRPC server port")
	cmd.Flags().Uint64Var(&f.maxInMemLogSize, "max-in-mem-log-size", 0,
		"Per-shard in-memory log size limit in bytes before rate limiting (0=disabled)")
	cmd.Flags().Uint64Var(&f.maxTotalInMemLogSize, "max-total-in-mem-log-size", 0,
		"Global in-memory log size limit across all shards in bytes (0=disabled)")
	cmd.Flags().StringVar(&f.logFormat, "log-format", "text", "Log output format: text, json")
	cmd.Flags().StringVar(&f.logLevel, "log-level", "info", "Minimum log level: debug, info, warn, error")
	cmd.Flags().BoolVar(&f.debugRaft, "debug-raft", false, "Enable verbose Raft protocol logging (sets log level to debug)")
	cmd.Flags().Uint64Var(&f.snapshotEntries, "snapshot-entries", 0,
		"Number of applied entries between automatic snapshots (0=disabled)")
	cmd.Flags().Uint64Var(&f.compactionOverhead, "compaction-overhead", 0,
		"Number of entries to keep after compaction (0=use default 5000)")
	cmd.Flags().BoolVar(&f.join, "join", false,
		"Join an existing cluster (for node replacement)")
	cmd.Flags().BoolVar(&f.enableREST, "enable-rest", true, "Enable REST API server")
	cmd.Flags().BoolVar(&f.enableGRPC, "enable-grpc", true, "Enable gRPC API server")
	cmd.Flags().StringVar(&f.tlsCAFile, "tls-ca-file", "", "Path to CA certificate PEM file")
	cmd.Flags().StringVar(&f.tlsCertFile, "tls-cert-file", "", "Path to TLS certificate PEM file")
	cmd.Flags().StringVar(&f.tlsKeyFile, "tls-key-file", "", "Path to TLS private key PEM file")

	return cmd
}

// applyConfigToFlags applies YAML configuration values to startFlags for
// fields that were not explicitly set via CLI flags. CLI flags always take
// precedence over YAML values.
func applyConfigToFlags(cmd *cobra.Command, f *startFlags, cfg *qrdbconfig.Config) {
	if !cmd.Flags().Changed("listen") {
		f.listen = cfg.Server.Host + ":" + strconv.Itoa(cfg.Server.AdminPort)
	}
	if !cmd.Flags().Changed("raft-address") {
		f.raftAddress = cfg.Raft.RaftAddress
	}
	if !cmd.Flags().Changed("data-dir") {
		f.dataDir = cfg.Raft.DataDir
	}
	if !cmd.Flags().Changed("deployment-id") {
		f.deploymentID = cfg.Raft.DeploymentID
	}
	if !cmd.Flags().Changed("sm") {
		f.smType = cfg.Raft.SMType
	}
	if !cmd.Flags().Changed("discovery") {
		f.discoveryMethod = cfg.Discovery.Method
	}
	if !cmd.Flags().Changed("peers") && cfg.Raft.Peers != "" {
		f.peers = strings.Split(cfg.Raft.Peers, ",")
	}
	if !cmd.Flags().Changed("shard-id") {
		f.shardID = cfg.Raft.ShardID
	}
	if !cmd.Flags().Changed("replica-id") {
		f.replicaID = cfg.Raft.ReplicaID
	}
	if !cmd.Flags().Changed("multicast-secret") && cfg.Discovery.MulticastSecret != "" {
		f.multicastSecret = cfg.Discovery.MulticastSecret
	}
	if !cmd.Flags().Changed("dns-domain") && cfg.Discovery.DNSDomain != "" {
		f.dnsDomain = cfg.Discovery.DNSDomain
	}
	if !cmd.Flags().Changed("admin-port") {
		f.adminPort = cfg.Server.AdminPort
	}
	if !cmd.Flags().Changed("grpc-port") {
		f.grpcPort = cfg.Server.GRPCPort
	}
	if !cmd.Flags().Changed("max-in-mem-log-size") {
		f.maxInMemLogSize = cfg.Raft.MaxInMemLogSize
	}
	if !cmd.Flags().Changed("max-total-in-mem-log-size") {
		f.maxTotalInMemLogSize = cfg.Raft.MaxTotalInMemLogSize
	}
	if !cmd.Flags().Changed("snapshot-entries") {
		f.snapshotEntries = cfg.Raft.SnapshotEntries
	}
	if !cmd.Flags().Changed("compaction-overhead") {
		f.compactionOverhead = cfg.Raft.CompactionOverhead
	}
	if !cmd.Flags().Changed("join") {
		f.join = cfg.Raft.Join
	}
	if !cmd.Flags().Changed("log-format") {
		f.logFormat = cfg.Logging.Format
	}
	if !cmd.Flags().Changed("log-level") {
		f.logLevel = cfg.Logging.Level
	}
	if !cmd.Flags().Changed("enable-rest") {
		f.enableREST = cfg.Protocols.REST
	}
	if !cmd.Flags().Changed("enable-grpc") {
		f.enableGRPC = cfg.Protocols.GRPC
	}
	if !cmd.Flags().Changed("tls-ca-file") && cfg.TLS.CAFile != "" {
		f.tlsCAFile = cfg.TLS.CAFile
	}
	if !cmd.Flags().Changed("tls-cert-file") && cfg.TLS.CertFile != "" {
		f.tlsCertFile = cfg.TLS.CertFile
	}
	if !cmd.Flags().Changed("tls-key-file") && cfg.TLS.KeyFile != "" {
		f.tlsKeyFile = cfg.TLS.KeyFile
	}
}

// resolveAutoConfig auto-generates replica-id, port, and data-dir for
// non-static discovery methods when the user hasn't explicitly set them.
// This allows users to run `qrdbd start --discovery multicast --multicast-secret test`
// in multiple terminals without manually assigning unique values.
func resolveAutoConfig(cmd *cobra.Command, f *startFlags) error {
	if f.discoveryMethod == "static" {
		return nil
	}

	// Auto-generate replica-id if not explicitly set.
	if !cmd.Flags().Changed("replica-id") && f.configFile == "" {
		id, err := generateReplicaID()
		if err != nil {
			return err
		}
		f.replicaID = id
		slog.Info("auto-generated replica ID", "replica_id", f.replicaID)
	}

	// Auto-select a free port if listen/raft-address use the default port.
	if !cmd.Flags().Changed("listen") && !cmd.Flags().Changed("raft-address") && f.configFile == "" {
		port, err := findFreeUDPPort()
		if err != nil {
			return err
		}
		f.listen = fmt.Sprintf("0.0.0.0:%d", port)
		f.raftAddress = fmt.Sprintf("127.0.0.1:%d", port)
		slog.Info("auto-selected port", "port", port)
	}

	// Auto-select admin port if not explicitly set.
	if !cmd.Flags().Changed("admin-port") && f.configFile == "" {
		port, err := findFreeTCPPort()
		if err != nil {
			return err
		}
		f.adminPort = port
		slog.Info("auto-selected admin port", "admin_port", port)
	}

	// Auto-select gRPC port if not explicitly set.
	if !cmd.Flags().Changed("grpc-port") && f.configFile == "" {
		port, err := findFreeTCPPort()
		if err != nil {
			return err
		}
		f.grpcPort = port
		slog.Info("auto-selected gRPC port", "grpc_port", port)
	}

	// Append replica-id to data-dir to avoid collisions when not explicitly set.
	if !cmd.Flags().Changed("data-dir") && f.configFile == "" {
		f.dataDir = fmt.Sprintf("./data/%d", f.replicaID)
		slog.Info("auto-configured data directory", "data_dir", f.dataDir)
	}

	return nil
}

// generateReplicaID generates a random replica ID using crypto/rand.
// The result is masked to 48 bits to keep it human-readable while still
// having negligible collision probability.
func generateReplicaID() (uint64, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return 0, err
	}
	// Mask to 48 bits and ensure non-zero.
	id := binary.LittleEndian.Uint64(buf[:]) & 0xFFFFFFFFFFFF
	if id == 0 {
		id = 1
	}
	return id, nil
}

// findFreeUDPPort finds and returns an available UDP port on localhost.
func findFreeUDPPort() (int, error) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := pc.LocalAddr().(*net.UDPAddr).Port
	_ = pc.Close()
	return port, nil
}

// findFreeTCPPort finds and returns an available TCP port on localhost.
func findFreeTCPPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port, nil
}

// runStart executes the start command logic: load or generate TLS, build
// config, create the host, start the shard, build service layer, create
// the multi-protocol server, and block until a termination signal is
// received.
func runStart(f *startFlags) error {
	// Load TLS from files or auto-generate a CA + leaf certificate.
	var bundle *TLSBundle
	var err error
	if f.tlsCAFile != "" && f.tlsCertFile != "" && f.tlsKeyFile != "" {
		bundle, err = loadTLSFromFiles(f.tlsCAFile, f.tlsCertFile, f.tlsKeyFile)
	} else {
		bundle, err = generateCATLS()
		if err == nil {
			// Write the CA cert to {data-dir}/ca.pem so CLI commands
			// (status, peers) can verify the server certificate.
			caPath := filepath.Join(f.dataDir, "ca.pem")
			if mkErr := os.MkdirAll(f.dataDir, 0o700); mkErr != nil {
				return &CACertWriteError{Path: caPath, Err: mkErr}
			}
			if writeErr := os.WriteFile(caPath, bundle.CACertPEM, 0o600); writeErr != nil {
				return &CACertWriteError{Path: caPath, Err: writeErr}
			}
			slog.Info("auto-generated CA certificate written", "path", caPath)
		}
	}
	if err != nil {
		return err
	}

	host, err := initializeNode(f, bundle)
	if err != nil {
		return err
	}

	// Build the service layer.
	kvSvc := service.NewKVService(host, f.shardID)
	adminSvc := service.NewAdminService(host)

	// Build leader forwarding resolver so writes on non-leader nodes
	// are transparently proxied to the current leader. Uses HTTPS scheme
	// since the admin server now runs TLS.
	adminPortStr := strconv.Itoa(f.adminPort)
	registry := rest.NewPeerAdminRegistryTLS(adminPortStr)

	// Register the local node so other nodes forwarding back to us can
	// be detected as a loop (and for symmetry with peer registrations).
	localAdminURL := "https://" + net.JoinHostPort("127.0.0.1", adminPortStr)
	registry.Register(f.raftAddress, localAdminURL)

	// Build server configuration using the QRDB config type required by
	// the server.New constructor.
	serverCfg := buildServerConfig(f)

	// Create and start the multi-protocol server.
	srv := server.New(serverCfg, kvSvc, adminSvc,
		server.WithTLS(bundle.TLSConfig),
		server.WithCACertPool(bundle.CACertPool),
		server.WithLeaderForwarding(registry),
		server.WithHost(host),
	)
	if err := srv.Start(); err != nil {
		_ = shutdownNode(host)
		return err
	}

	slog.Info("qrdbd node started, waiting for signals")

	// Block until SIGINT or SIGTERM.
	waitForSignal()

	srv.Stop()
	return shutdownNode(host)
}

// buildServerConfig constructs a *qrdbconfig.Config from the parsed start
// flags for use by the multi-protocol server orchestrator.
func buildServerConfig(f *startFlags) *qrdbconfig.Config {
	return &qrdbconfig.Config{
		Server: qrdbconfig.ServerConfig{
			AdminPort: f.adminPort,
			GRPCPort:  f.grpcPort,
			Host:      "0.0.0.0",
		},
		Protocols: qrdbconfig.ProtocolsConfig{
			REST: f.enableREST,
			GRPC: f.enableGRPC,
		},
		Raft: qrdbconfig.RaftConfig{
			DataDir:              f.dataDir,
			DeploymentID:         f.deploymentID,
			ShardID:              f.shardID,
			ReplicaID:            f.replicaID,
			RaftAddress:          f.raftAddress,
			ListenAddress:        f.listen,
			SMType:               f.smType,
			MaxInMemLogSize:      f.maxInMemLogSize,
			MaxTotalInMemLogSize: f.maxTotalInMemLogSize,
			SnapshotEntries:      f.snapshotEntries,
			CompactionOverhead:   f.compactionOverhead,
			Join:                 f.join,
		},
		TLS: qrdbconfig.TLSConfig{
			Enabled:    true,
			CAFile:     f.tlsCAFile,
			CertFile:   f.tlsCertFile,
			KeyFile:    f.tlsKeyFile,
			ClientAuth: "require_and_verify",
			MinVersion: "TLS1.3",
		},
		Logging: qrdbconfig.LoggingConfig{
			Level:  f.logLevel,
			Format: f.logFormat,
		},
		Discovery: qrdbconfig.DiscoveryConfig{
			Method:          f.discoveryMethod,
			MulticastSecret: f.multicastSecret,
			DNSDomain:       f.dnsDomain,
		},
	}
}

// initializeNode performs all initialization: validates flags, creates the
// host, and starts the shard. Returns the running host or an error.
// The bundle is used to configure mTLS on the QUIC transport for
// multi-node clusters (nil for single-node which uses WithoutTransport).
func initializeNode(f *startFlags, bundle *TLSBundle) (*quicraft.Host, error) {
	slog.Info("starting qrdbd node",
		"listen", f.listen,
		"raft_address", f.raftAddress,
		"data_dir", f.dataDir,
		"shard_id", f.shardID,
		"replica_id", f.replicaID,
		"sm", f.smType,
		"discovery", f.discoveryMethod,
	)

	// Resolve the state machine factory.
	createFn, err := resolveCreateFunc(f.smType)
	if err != nil {
		return nil, err
	}

	// Validate peers early for static multi-node clusters so that
	// PeerParseError surfaces before transport initialization which
	// requires mTLS and would produce a less helpful error.
	if f.discoveryMethod == "static" && !isSingleNode(f) {
		if _, err := parsePeers(f.peers); err != nil {
			return nil, err
		}
	}

	// Build mTLS config for transport (nil for single-node).
	var mtlsCfg *quicraftconfig.MTLSConfig
	if !isSingleNode(f) && bundle != nil {
		mtlsCfg = &quicraftconfig.MTLSConfig{
			CACert: bundle.CACertPEM,
			Cert:   bundle.CertPEM,
			Key:    bundle.KeyPEM,
		}
	}

	// Build host configuration.
	hostCfg := buildHostConfig(f, mtlsCfg)

	// Determine host options based on peer count.
	// Single-node (no remote peers) uses WithoutTransport.
	var hostOpts []quicraft.HostOption
	if isSingleNode(f) {
		hostOpts = append(hostOpts, quicraft.WithoutTransport())
		slog.Info("single-node mode, transport disabled")
	}

	// Create the host.
	host, err := quicraft.NewHost(hostCfg, hostOpts...)
	if err != nil {
		return nil, err
	}

	// Build shard configuration.
	shardCfg := buildShardConfig(f)

	// Start the shard using the selected discovery method.
	if err := startShard(f, host, createFn, shardCfg); err != nil {
		_ = host.Close()
		return nil, err
	}

	return host, nil
}

// waitForSignal blocks until SIGINT or SIGTERM is received.
func waitForSignal() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	slog.Info("received signal, shutting down", "signal", sig.String())
}

// shutdownNode gracefully closes the node and logs the result.
func shutdownNode(node io.Closer) error {
	if err := node.Close(); err != nil {
		slog.Error("error during shutdown", "error", err)
		return err
	}
	slog.Info("qrdbd node stopped")
	return nil
}

// resolveCreateFunc looks up the state machine factory by name and returns
// the CreateFunc, or an error if the name is unknown.
func resolveCreateFunc(smType string) (sm.CreateFunc, error) {
	factory, ok := smFactories[smType]
	if !ok {
		return nil, &UnknownSMError{Name: smType}
	}
	return factory(), nil
}

// buildHostConfig constructs a HostConfig from the parsed start flags.
// When mtls is non-nil it is attached to the TransportConfig for QUIC
// transport mTLS.
func buildHostConfig(f *startFlags, mtls *quicraftconfig.MTLSConfig) quicraftconfig.HostConfig {
	walDir := filepath.Join(f.dataDir, "wal")
	nodeHostDir := filepath.Join(f.dataDir, "nodehost")

	return quicraftconfig.HostConfig{
		WALDir:               walDir,
		NodeHostDir:          nodeHostDir,
		RaftAddress:          f.raftAddress,
		ListenAddress:        f.listen,
		DeploymentID:         f.deploymentID,
		MaxTotalInMemLogSize: f.maxTotalInMemLogSize,
		TransportConfig: quicraftconfig.TransportConfig{
			MTLSConfig: mtls,
		},
		LogConfig: quicraftconfig.LogConfig{
			Format: quicraftconfig.LogFormat(f.logFormat),
			Level:  quicraftconfig.LogLevel(f.logLevel),
		},
	}
}

// buildShardConfig constructs a shard Config from the parsed start flags.
func buildShardConfig(f *startFlags) quicraftconfig.Config {
	return quicraftconfig.Config{
		ShardID:            f.shardID,
		ReplicaID:          f.replicaID,
		MaxInMemLogSize:    f.maxInMemLogSize,
		SnapshotEntries:    f.snapshotEntries,
		CompactionOverhead: f.compactionOverhead,
	}
}

// startShard starts the Raft shard using the configured discovery method.
func startShard(f *startFlags, node raftNode, createFn sm.CreateFunc, shardCfg quicraftconfig.Config) error {
	if f.discoveryMethod == "static" {
		return startShardStatic(f, node, createFn, shardCfg)
	}
	return startShardWithBootstrap(f, node, createFn, shardCfg)
}

// startShardStatic starts a shard with static discovery by parsing the
// --peers flag directly into a member map.
func startShardStatic(f *startFlags, node raftNode, createFn sm.CreateFunc, shardCfg quicraftconfig.Config) error {
	members, err := parsePeers(f.peers)
	if err != nil {
		return err
	}

	if len(members) == 0 {
		return &NoPeersError{}
	}

	slog.Info("starting shard with static peers",
		"shard_id", f.shardID,
		"members", len(members),
	)

	return node.StartShard(members, f.join, createFn, shardCfg)
}

// startShardWithBootstrap starts a shard using the bootstrapper with the
// configured discovery method.
func startShardWithBootstrap(f *startFlags, node raftNode, createFn sm.CreateFunc, shardCfg quicraftconfig.Config) error {
	builder, ok := discoveryBuilders[f.discoveryMethod]
	if !ok {
		return &UnknownDiscoveryError{Name: f.discoveryMethod}
	}

	disc, err := builder(f)
	if err != nil {
		return err
	}

	bootstrapCfg := bootstrap.Config{
		NodeID:      f.replicaID,
		Address:     f.raftAddress,
		ShardID:     f.shardID,
		ReplicaID:   f.replicaID,
		Discovery:   disc,
		CreateFn:    createFn,
		ShardConfig: shardCfg,
	}

	b := bootstrap.NewBootstrapper(bootstrapCfg, node)

	slog.Info("bootstrapping shard",
		"shard_id", f.shardID,
		"discovery", f.discoveryMethod,
	)

	return b.Bootstrap()
}

// buildStaticDiscovery creates a StaticDiscovery from the parsed peers.
func buildStaticDiscovery(f *startFlags) (discovery.Method, error) {
	members, err := parsePeers(f.peers)
	if err != nil {
		return nil, err
	}

	peers := make([]discovery.Peer, 0, len(members))
	for id, addr := range members {
		peers = append(peers, discovery.Peer{NodeID: id, Address: addr})
	}

	return discovery.NewStaticDiscovery(discovery.StaticConfig{Peers: peers}), nil
}

// buildMulticastDiscovery creates a MulticastDiscovery from the flags.
func buildMulticastDiscovery(f *startFlags) (discovery.Method, error) {
	if f.multicastSecret == "" {
		return nil, &MissingFlagError{Flag: "multicast-secret"}
	}

	cfg := discovery.MulticastConfig{
		NodeID:       f.replicaID,
		Address:      f.raftAddress,
		DeploymentID: f.deploymentID,
		SharedSecret: []byte(f.multicastSecret),
		MinPeers:     defaultMinPeers - 1, // other peers excluding self
	}

	md, err := discovery.NewMulticastDiscovery(cfg)
	if err != nil {
		return nil, err
	}
	md.Start()

	return md, nil
}

// defaultMinPeers is the default minimum cluster size for bootstrap.
const defaultMinPeers = 3

// buildDNSDiscovery creates a DNSDiscovery from the flags.
func buildDNSDiscovery(f *startFlags) (discovery.Method, error) {
	if f.dnsDomain == "" {
		return nil, &MissingFlagError{Flag: "dns-domain"}
	}

	cfg := discovery.DNSConfig{
		Service: "_raft",
		Proto:   "_udp",
		Domain:  f.dnsDomain,
	}

	return discovery.NewDNSDiscovery(cfg), nil
}

// parsePeers parses a slice of "id=address" strings into a member map.
func parsePeers(peers []string) (map[uint64]string, error) {
	members := make(map[uint64]string, len(peers))
	for _, p := range peers {
		idx := strings.IndexByte(p, '=')
		if idx < 1 {
			return nil, &PeerParseError{Input: p}
		}

		id, err := strconv.ParseUint(p[:idx], 10, 64)
		if err != nil {
			return nil, &PeerParseError{Input: p}
		}

		addr := p[idx+1:]
		if addr == "" {
			return nil, &PeerParseError{Input: p}
		}

		members[id] = addr
	}
	return members, nil
}

// isSingleNode returns true when the peer configuration describes a
// single-node cluster (only the local node). Non-static discovery
// methods always assume multi-node.
func isSingleNode(f *startFlags) bool {
	if f.discoveryMethod != "static" {
		return false
	}
	if len(f.peers) == 0 {
		return true
	}
	if len(f.peers) == 1 {
		members, err := parsePeers(f.peers)
		if err != nil {
			return false
		}
		return len(members) == 1
	}
	return false
}
