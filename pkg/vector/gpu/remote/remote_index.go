// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

// Package remote provides an interfaces.HNSWIndex implementation that
// delegates all vector index operations to a remote VectorIndexService over
// gRPC, allowing the GPU workload to run on a dedicated GPU host while the
// main qrdb process runs locally.
package remote

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"os"
	"strconv"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/jeremyhahn/go-qrdb/pkg/interfaces"
	proto "github.com/jeremyhahn/go-qrdb/pkg/vector/gpu/remote/proto"
)

// Config carries the connection parameters needed by RemoteGPUIndex.
// It mirrors gpu.RemoteGPUConfig field-for-field so that the gpu package can
// convert without introducing a circular import.
type Config struct {
	// Endpoint is the host:port of the remote VectorIndexService,
	// e.g. "gpu-host.example.com:9090".
	Endpoint string

	// TLSCert is the path to the PEM-encoded client certificate for mTLS.
	TLSCert string

	// TLSKey is the path to the PEM-encoded private key for the client cert.
	TLSKey string

	// TLSCA is the path to the PEM-encoded CA certificate for server verification.
	TLSCA string

	// TimeoutMs is the per-call deadline in milliseconds.
	// 0 means no deadline.
	TimeoutMs int
}

// RemoteGPUIndex implements interfaces.HNSWIndex by forwarding every operation
// to a remote VectorIndexService via gRPC. The connection is established once
// during construction and reused for all subsequent calls.
type RemoteGPUIndex struct {
	config *Config
	conn   *grpc.ClientConn
	client proto.VectorIndexServiceClient
}

// NewRemoteGPUIndex dials the remote VectorIndexService described by config and
// returns a ready-to-use RemoteGPUIndex. The caller is responsible for calling
// Close when the index is no longer needed.
func NewRemoteGPUIndex(config *Config) (*RemoteGPUIndex, error) {
	if config == nil {
		return nil, ErrNilConfig
	}
	if config.Endpoint == "" {
		return nil, ErrEmptyEndpoint
	}

	dialOpts, err := buildDialOptions(config)
	if err != nil {
		return nil, &IndexError{Op: "build dial options", Cause: err}
	}

	conn, err := grpc.NewClient(config.Endpoint, dialOpts...)
	if err != nil {
		return nil, &IndexError{Op: "dial " + config.Endpoint, Cause: err}
	}

	return &RemoteGPUIndex{
		config: config,
		conn:   conn,
		client: newVectorIndexServiceClient(conn),
	}, nil
}

// Insert adds a vector with the given ID to the remote index.
func (r *RemoteGPUIndex) Insert(id uint64, vector []float32) error {
	ctx, cancel := r.callContext()
	defer cancel()

	_, err := r.client.Insert(ctx, &proto.InsertRequest{
		ID:     id,
		Vector: vector,
	})
	if err != nil {
		return &IndexError{Op: "insert id=" + strconv.FormatUint(id, 10), Cause: err}
	}
	return nil
}

// Delete removes a vector by ID from the remote index.
func (r *RemoteGPUIndex) Delete(id uint64) error {
	ctx, cancel := r.callContext()
	defer cancel()

	_, err := r.client.Delete(ctx, &proto.DeleteRequest{ID: id})
	if err != nil {
		return &IndexError{Op: "delete id=" + strconv.FormatUint(id, 10), Cause: err}
	}
	return nil
}

// Search performs k-nearest neighbour search on the remote index and returns
// up to k results sorted by distance (ascending).
func (r *RemoteGPUIndex) Search(query []float32, k int) ([]*interfaces.HNSWSearchResult, error) {
	ctx, cancel := r.callContext()
	defer cancel()

	resp, err := r.client.Search(ctx, &proto.SearchRequest{
		Query: query,
		K:     int32(k),
	})
	if err != nil {
		return nil, &IndexError{Op: "search k=" + strconv.Itoa(k), Cause: err}
	}

	results := make([]*interfaces.HNSWSearchResult, len(resp.Results))
	for i, sr := range resp.Results {
		results[i] = &interfaces.HNSWSearchResult{
			ID:       sr.ID,
			Distance: sr.Distance,
		}
	}
	return results, nil
}

// Count returns the number of vectors currently in the remote index.
// A failed RPC returns 0; the error is silently discarded to satisfy the
// interfaces.HNSWIndex contract which returns no error from Count.
func (r *RemoteGPUIndex) Count() int64 {
	ctx, cancel := r.callContext()
	defer cancel()

	resp, err := r.client.Count(ctx, &proto.CountRequest{})
	if err != nil {
		return 0
	}
	return resp.Count
}

// GetStats returns index statistics from the remote service.
// A failed RPC returns a zero-value HNSWStats.
func (r *RemoteGPUIndex) GetStats() *interfaces.HNSWStats {
	ctx, cancel := r.callContext()
	defer cancel()

	resp, err := r.client.GetStats(ctx, &proto.StatsRequest{})
	if err != nil {
		return &interfaces.HNSWStats{}
	}
	return &interfaces.HNSWStats{
		NodeCount:  resp.Count,
		Dimensions: int(resp.Dimensions),
	}
}

// Close releases the underlying gRPC connection.
func (r *RemoteGPUIndex) Close() error {
	if r.conn != nil {
		return r.conn.Close()
	}
	return nil
}

// callContext returns a context with the configured per-call deadline.
// If TimeoutMs is zero the returned context has no deadline.
func (r *RemoteGPUIndex) callContext() (context.Context, context.CancelFunc) {
	if r.config.TimeoutMs > 0 {
		return context.WithTimeout(
			context.Background(),
			time.Duration(r.config.TimeoutMs)*time.Millisecond,
		)
	}
	return context.WithCancel(context.Background())
}

// buildDialOptions constructs gRPC dial options from Config.
// mTLS is enabled when TLSCert, TLSKey, and TLSCA are all non-empty.
// When all TLS fields are empty the connection is insecure.
func buildDialOptions(config *Config) ([]grpc.DialOption, error) {
	hasCert := config.TLSCert != "" && config.TLSKey != ""
	hasCA := config.TLSCA != ""

	if !hasCert && !hasCA {
		return []grpc.DialOption{
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		}, nil
	}

	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS13,
	}

	if hasCert {
		cert, err := tls.LoadX509KeyPair(config.TLSCert, config.TLSKey)
		if err != nil {
			return nil, &TLSConfigError{Op: "load client key pair", Cause: err}
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}

	if hasCA {
		caPEM, err := os.ReadFile(config.TLSCA)
		if err != nil {
			return nil, &TLSConfigError{Op: "read CA cert " + config.TLSCA, Cause: err}
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, &CACertParseError{Path: config.TLSCA}
		}
		tlsCfg.RootCAs = pool
	}

	return []grpc.DialOption{
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
	}, nil
}
