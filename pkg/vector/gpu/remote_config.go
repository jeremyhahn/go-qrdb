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

package gpu

// RemoteGPUConfig configures the connection to a remote GPU index service.
// When Enabled is true, the GPUIndexFactory creates a RemoteGPUIndex that
// delegates all operations to the service at Endpoint rather than driving
// local CUDA hardware.
type RemoteGPUConfig struct {
	// Enabled activates remote GPU execution. When false the local GPU path
	// (or CPU fallback) is used instead.
	Enabled bool `json:"enabled" yaml:"enabled"`

	// Endpoint is the host:port address of the remote VectorIndexService,
	// e.g. "198.51.100.91:9090".
	Endpoint string `json:"endpoint" yaml:"endpoint"`

	// TLSCert is the path to the PEM-encoded client certificate used for mTLS.
	// Leave empty to disable client certificates.
	TLSCert string `json:"tls_cert" yaml:"tls_cert"`

	// TLSKey is the path to the PEM-encoded private key for the client certificate.
	TLSKey string `json:"tls_key" yaml:"tls_key"`

	// TLSCA is the path to the PEM-encoded CA certificate used to verify the
	// server certificate.
	TLSCA string `json:"tls_ca" yaml:"tls_ca"`

	// Protocol selects the transport used for remote calls.
	// Supported values: "grpc" (default).
	Protocol string `json:"protocol" yaml:"protocol"`

	// TimeoutMs is the per-call deadline in milliseconds.
	// 0 means no deadline (not recommended for production).
	TimeoutMs int `json:"timeout_ms" yaml:"timeout_ms"`
}

// DefaultRemoteGPUConfig returns a RemoteGPUConfig with safe defaults.
// Remote execution is disabled by default.
func DefaultRemoteGPUConfig() *RemoteGPUConfig {
	return &RemoteGPUConfig{
		Enabled:   false,
		Endpoint:  "",
		Protocol:  "grpc",
		TimeoutMs: 5000,
	}
}
