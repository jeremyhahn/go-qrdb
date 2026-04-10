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

package driver

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql/driver"
	"net/http"
	"os"
)

// Compile-time interface assertion.
var _ driver.Connector = (*NetworkConnector)(nil)

// NetworkConnector creates connections that forward SQL queries to a
// remote qrdb server via REST or gRPC.
type NetworkConnector struct {
	cfg    *DSNConfig
	client *http.Client
}

// NewNetworkConnector creates a new NetworkConnector from the given
// DSN configuration. An HTTP client is created with optional TLS
// configuration derived from the DSN.
func NewNetworkConnector(cfg *DSNConfig) *NetworkConnector {
	client := &http.Client{}

	// Configure TLS if certificate paths are provided.
	if cfg.CAFile != "" || cfg.CertFile != "" {
		tlsCfg := &tls.Config{}

		if cfg.CAFile != "" {
			caCert, err := os.ReadFile(cfg.CAFile)
			if err == nil {
				pool := x509.NewCertPool()
				pool.AppendCertsFromPEM(caCert)
				tlsCfg.RootCAs = pool
			}
		}

		if cfg.CertFile != "" && cfg.KeyFile != "" {
			cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
			if err == nil {
				tlsCfg.Certificates = []tls.Certificate{cert}
			}
		}

		client.Transport = &http.Transport{TLSClientConfig: tlsCfg}
	}

	return &NetworkConnector{
		cfg:    cfg,
		client: client,
	}
}

// Connect returns a new network connection to the remote qrdb server.
func (c *NetworkConnector) Connect(_ context.Context) (driver.Conn, error) {
	baseURL := c.cfg.Scheme + "://" + c.cfg.Host

	return &networkConn{
		client:   c.client,
		baseURL:  baseURL,
		database: c.cfg.Database,
	}, nil
}

// Driver returns the driver that created this connector.
func (c *NetworkConnector) Driver() driver.Driver {
	return &Driver{}
}
