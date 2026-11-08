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

package config

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// providerMapsFile holds the free-form maps whose keys belong to a
// provider, not to qrdb, as the YAML file spells them. Only the keys are
// read; the values come from viper, so environment overrides still apply.
type providerMapsFile struct {
	Storage struct {
		Objstore struct {
			Settings map[string]any `yaml:"settings"`
		} `yaml:"objstore"`
		VolumePools []struct {
			Options map[string]any `yaml:"options"`
		} `yaml:"volume_pools"`
	} `yaml:"storage"`
}

// restoreProviderKeyCase gives storage.objstore.settings and each
// storage.volume_pools[].options back the key spelling of the YAML file.
// Viper lowercases every key, which would hide camelCase provider keys
// such as go-objstore's forcePathStyle from the provider.
func restoreProviderKeyCase(cfg *Config, data []byte) error {
	var file providerMapsFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		return err
	}
	settings, err := restoreKeyCase("storage.objstore.settings", file.Storage.Objstore.Settings, cfg.Storage.Objstore.Settings)
	if err != nil {
		return err
	}
	cfg.Storage.Objstore.Settings = settings
	for i, pool := range file.Storage.VolumePools {
		if i >= len(cfg.Storage.VolumePools) {
			break
		}
		field := "storage.volume_pools[" + strconv.Itoa(i) + "].options"
		options, err := restoreKeyCase(field, pool.Options, cfg.Storage.VolumePools[i].Options)
		if err != nil {
			return err
		}
		cfg.Storage.VolumePools[i].Options = options
	}
	return nil
}

// restoreKeyCase rebuilds decoded, whose keys viper lowercased, with the
// keys of fileMap: out[k] = decoded[strings.ToLower(k)]. Two file keys
// that differ only in case are refused, since viper merged them into one.
func restoreKeyCase(field string, fileMap map[string]any, decoded map[string]string) (map[string]string, error) {
	if len(fileMap) == 0 {
		return decoded, nil
	}
	keys := make([]string, 0, len(fileMap))
	for k := range fileMap {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	seen := make(map[string]string, len(keys))
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		lower := strings.ToLower(k)
		if prev, ok := seen[lower]; ok {
			return nil, fmt.Errorf("%s: keys %q and %q differ only in case", field, prev, k)
		}
		seen[lower] = k
		if v, ok := decoded[lower]; ok {
			out[k] = v
		}
	}
	return out, nil
}
