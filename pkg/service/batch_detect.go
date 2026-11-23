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
	"encoding/json"
	"strings"
)

// importExtensionFormats maps a lowercase file extension to the import
// format parseAndImport parses. Only formats the import can read are
// listed: detecting a format only to refuse it helps nobody.
var importExtensionFormats = map[string]string{
	".json":   "json",
	".csv":    "csv",
	".yaml":   "yaml",
	".yml":    "yaml",
	".zip":    "zip",
	".tar":    "tar",
	".tgz":    "tar.gz",
	".ndjson": "ndjson",
	".jsonl":  "ndjson",
}

// importFormatDetectionError reports that an import's format could be
// found neither from its file name nor from its content.
type importFormatDetectionError struct {
	filename string
}

func (e *importFormatDetectionError) Error() string {
	return "import: unable to detect format for " + e.filename
}

// detectImportFormat resolves the import format of an upload: the
// compound .tar.gz extension, then the single extension, then archive
// magic bytes, then JSON content. sample is the head of the upload.
func detectImportFormat(filename string, sample []byte) (string, error) {
	if strings.HasSuffix(strings.ToLower(filename), ".tar.gz") {
		return "tar.gz", nil
	}
	if dot := strings.LastIndex(filename, "."); dot >= 0 {
		if format, ok := importExtensionFormats[strings.ToLower(filename[dot:])]; ok {
			return format, nil
		}
	}
	if format, ok := detectArchiveMagic(sample); ok {
		return format, nil
	}
	if len(sample) > 0 && json.Valid(sample) {
		return "json", nil
	}
	return "", &importFormatDetectionError{filename: filename}
}

// detectArchiveMagic recognises ZIP, gzip (taken as tar.gz) and tar
// archives by their signatures.
func detectArchiveMagic(data []byte) (string, bool) {
	if len(data) < 2 {
		return "", false
	}
	// ZIP: PK followed by a local file header, end-of-central-directory
	// or data descriptor marker.
	if len(data) >= 4 && data[0] == 0x50 && data[1] == 0x4B {
		switch [2]byte{data[2], data[3]} {
		case [2]byte{0x03, 0x04}, [2]byte{0x05, 0x06}, [2]byte{0x07, 0x08}:
			return "zip", true
		}
	}
	// gzip: \x1f\x8b.
	if data[0] == 0x1F && data[1] == 0x8B {
		return "tar.gz", true
	}
	// tar: "ustar" at offset 257.
	if len(data) >= 262 && string(data[257:262]) == "ustar" {
		return "tar", true
	}
	return "", false
}

// importFormats is the one list of batch import file formats: "auto"
// detects the format from the filename and content (detectImportFormat),
// the rest name a format explicitly.
var importFormats = []string{"auto", "csv", "json", "ndjson", "yaml", "archive"}

// ImportFormats returns the batch import file formats, "auto" first. The
// REST route GET /batch/import/formats and the desktop binding's
// AvailableImportFormats both report this list. The caller owns the
// returned slice.
func ImportFormats() []string {
	out := make([]string, len(importFormats))
	copy(out, importFormats)
	return out
}
