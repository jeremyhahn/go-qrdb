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

package service

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetectImportFormat_Extensions(t *testing.T) {
	cases := map[string]string{
		"data.json":     "json",
		"data.csv":      "csv",
		"data.yaml":     "yaml",
		"data.yml":      "yaml",
		"data.zip":      "zip",
		"data.tar":      "tar",
		"data.tgz":      "tar.gz",
		"data.tar.gz":   "tar.gz",
		"data.ndjson":   "ndjson",
		"data.jsonl":    "ndjson",
		"DATA.JSON":     "json",
		"Data.Yml":      "yaml",
		"x.json.tar.gz": "tar.gz",
	}
	for name, want := range cases {
		got, err := detectImportFormat(name, nil)
		require.NoError(t, err, name)
		assert.Equal(t, want, got, name)
	}
}

func TestDetectImportFormat_RefusedFormatsAreNotDetected(t *testing.T) {
	for _, name := range []string{"d.msgpack", "d.mp", "d.bson", "d.pb", "d.proto", "d.protobuf"} {
		_, err := detectImportFormat(name, []byte{0x00, 0x01, 0x02, 0x03})
		var fde *importFormatDetectionError
		require.True(t, errors.As(err, &fde), "%s: got %v", name, err)
	}
	// A BSON document's length prefix is no longer a magic number.
	bsonDoc := []byte{0x05, 0x00, 0x00, 0x00, 0x00}
	_, err := detectImportFormat("", bsonDoc)
	require.Error(t, err)
}

func TestDetectImportFormat_MagicBytes(t *testing.T) {
	for _, marker := range [][]byte{{0x03, 0x04}, {0x05, 0x06}, {0x07, 0x08}} {
		got, err := detectImportFormat("upload", append([]byte{0x50, 0x4B}, marker...))
		require.NoError(t, err)
		assert.Equal(t, "zip", got)
	}

	got, err := detectImportFormat("upload", []byte{0x1F, 0x8B, 0x08, 0x00})
	require.NoError(t, err)
	assert.Equal(t, "tar.gz", got)

	tarHeader := make([]byte, 512)
	copy(tarHeader[257:], "ustar")
	got, err = detectImportFormat("upload", tarHeader)
	require.NoError(t, err)
	assert.Equal(t, "tar", got)

	// PK without a record marker is not a ZIP.
	_, err = detectImportFormat("upload", []byte{0x50, 0x4B, 0x00, 0x00})
	require.Error(t, err)
}

func TestDetectImportFormat_Content(t *testing.T) {
	got, err := detectImportFormat("", []byte(`{"id":"1"}`))
	require.NoError(t, err)
	assert.Equal(t, "json", got)

	got, err = detectImportFormat("noext", []byte(`[{"id":"1"},{"id":"2"}]`))
	require.NoError(t, err)
	assert.Equal(t, "json", got)
}

func TestDetectImportFormat_Unknown(t *testing.T) {
	_, err := detectImportFormat("data.xyz", []byte{0x00, 0x01, 0x02, 0x03})
	var fde *importFormatDetectionError
	require.True(t, errors.As(err, &fde))
	assert.Equal(t, "data.xyz", fde.filename)
	assert.Contains(t, err.Error(), "data.xyz")

	_, err = detectImportFormat("", nil)
	require.True(t, errors.As(err, &fde))

	_, err = detectImportFormat("", []byte{0x01})
	require.Error(t, err)
}
