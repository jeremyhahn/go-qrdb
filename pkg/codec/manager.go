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

package codec

// Manager provides dual-codec encoding with separate storage and wire codecs.
// Storage codecs are optimized for compact binary serialization (default:
// msgpack). Wire codecs are optimized for human-readable interchange
// (default: json). The Manager also supports transcoding between the two
// formats in a single operation.
type Manager struct {
	storage Codec
	wire    Codec
}

// NewManager creates a Manager with the given storage and wire codecs.
func NewManager(storage, wire Codec) *Manager {
	return &Manager{
		storage: storage,
		wire:    wire,
	}
}

// NewManagerFromConfig creates a Manager from a Config, resolving codec
// names to implementations and optionally wrapping them with buffer
// pooling for optimized performance.
func NewManagerFromConfig(cfg *Config) (*Manager, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	storageCodec, err := ResolveCodec(cfg.StorageCodec)
	if err != nil {
		return nil, err
	}

	wireCodec, err := ResolveCodec(cfg.WireCodec)
	if err != nil {
		return nil, err
	}

	if cfg.UseOptimized {
		storageCodec = NewOptimizedCodec(storageCodec)
		wireCodec = NewOptimizedCodec(wireCodec)
	}

	return NewManager(storageCodec, wireCodec), nil
}

// EncodeForStorage marshals v using the storage codec.
func (m *Manager) EncodeForStorage(v any) ([]byte, error) {
	return m.storage.Marshal(v)
}

// DecodeFromStorage unmarshals data into v using the storage codec.
func (m *Manager) DecodeFromStorage(data []byte, v any) error {
	return m.storage.Unmarshal(data, v)
}

// EncodeForWire marshals v using the wire codec.
func (m *Manager) EncodeForWire(v any) ([]byte, error) {
	return m.wire.Marshal(v)
}

// DecodeFromWire unmarshals data into v using the wire codec.
func (m *Manager) DecodeFromWire(data []byte, v any) error {
	return m.wire.Unmarshal(data, v)
}

// Transcode decodes data from the storage codec into the provided
// intermediate value, then re-encodes it using the wire codec.
// The from parameter must be a pointer to the target type that the
// storage-encoded data represents.
func (m *Manager) Transcode(data []byte, from any) ([]byte, error) {
	if err := m.storage.Unmarshal(data, from); err != nil {
		return nil, &TranscodeError{
			FromCodec: m.storage.Type(),
			ToCodec:   m.wire.Type(),
			Err:       err,
		}
	}
	out, err := m.wire.Marshal(from)
	if err != nil {
		return nil, &TranscodeError{
			FromCodec: m.storage.Type(),
			ToCodec:   m.wire.Type(),
			Err:       err,
		}
	}
	return out, nil
}

// StorageCodec returns the underlying storage codec.
func (m *Manager) StorageCodec() Codec {
	return m.storage
}

// WireCodec returns the underlying wire codec.
func (m *Manager) WireCodec() Codec {
	return m.wire
}
