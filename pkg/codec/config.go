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

import "sort"

// codecRegistry maps codec type names to their factory functions.
// Codec implementations register themselves via init() in their respective
// build-tag-gated files.
var codecRegistry = make(map[string]func() Codec)

// RegisterCodec registers a codec factory function under the given name.
// It is called by each codec implementation's init() function and is safe
// to call during package initialization.
func RegisterCodec(name string, factory func() Codec) {
	codecRegistry[name] = factory
}

// AvailableCodecs returns a sorted list of codec type names that have been
// registered for the current build. The set depends on which codec build
// tags were active at compile time.
func AvailableCodecs() []string {
	names := make([]string, 0, len(codecRegistry))
	for name := range codecRegistry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Config holds codec configuration for storage and wire encoding.
type Config struct {
	StorageCodec string `yaml:"storage_codec"` // default: "msgpack"
	WireCodec    string `yaml:"wire_codec"`    // default: "json"
	UseOptimized bool   `yaml:"use_optimized"` // default: true
}

// DefaultConfig returns a Config with production defaults:
// msgpack for storage, json for wire, optimized buffer pooling enabled.
func DefaultConfig() *Config {
	return &Config{
		StorageCodec: TypeMsgPack,
		WireCodec:    TypeJSON,
		UseOptimized: true,
	}
}

// Validate checks that the configuration fields reference registered codecs.
// Only codecs compiled into the binary (via their build tags) are considered
// valid; referencing a codec that was excluded at build time returns an error.
func (c *Config) Validate() error {
	if c.StorageCodec == "" {
		return &ConfigValidationError{
			Field:   "storage_codec",
			Message: "must not be empty",
		}
	}
	if _, ok := codecRegistry[c.StorageCodec]; !ok {
		return &ConfigValidationError{
			Field:   "storage_codec",
			Message: "unsupported codec: " + c.StorageCodec,
		}
	}
	if c.WireCodec == "" {
		return &ConfigValidationError{
			Field:   "wire_codec",
			Message: "must not be empty",
		}
	}
	if _, ok := codecRegistry[c.WireCodec]; !ok {
		return &ConfigValidationError{
			Field:   "wire_codec",
			Message: "unsupported codec: " + c.WireCodec,
		}
	}
	return nil
}

// ResolveCodec returns a new Codec instance for the given type name.
// Returns an UnsupportedCodecError if the name is not registered for
// the current build.
func ResolveCodec(name string) (Codec, error) {
	factory, ok := codecRegistry[name]
	if !ok {
		return nil, &UnsupportedCodecError{Name: name}
	}
	return factory(), nil
}
