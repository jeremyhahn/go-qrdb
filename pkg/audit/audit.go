// Copyright (c) 2026 Jeremy Hahn
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

package audit

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// Logger defines the audit logging interface.
type Logger interface {
	// Log records an audit entry to the configured backends.
	Log(ctx context.Context, entry *Entry) error

	// Close releases any resources held by the logger.
	Close() error
}

// New creates a Logger based on the provided configuration. When both
// KV and file backends are enabled it returns a multiLogger that fans
// out entries to both. When audit is disabled or no backends are
// configured it returns a no-op logger. The kvStore parameter may be
// nil when cfg.KVBackend is false.
func New(cfg Config, kvStore kvstore.KVStore) (Logger, error) {
	if !cfg.Enabled {
		return &noopLogger{}, nil
	}

	var loggers []Logger

	if cfg.KVBackend {
		if kvStore == nil {
			return nil, &ConfigError{Detail: "kv_backend enabled but kvstore is nil"}
		}
		loggers = append(loggers, &kvLogger{store: kvStore})
	}

	if cfg.FileBackend {
		fl, err := newFileLogger(cfg.FilePath)
		if err != nil {
			return nil, err
		}
		loggers = append(loggers, fl)
	}

	if len(loggers) == 0 {
		return &noopLogger{}, nil
	}
	if len(loggers) == 1 {
		return loggers[0], nil
	}
	return &multiLogger{loggers: loggers}, nil
}
