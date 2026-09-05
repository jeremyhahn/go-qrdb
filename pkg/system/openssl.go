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

// Package system provides system-level utilities including OpenSSL configuration
// file parsing.
//
// The OpenSSL parser extracts default Subject values from the
// [req_distinguished_name] section of openssl.cnf files. It handles the
// INI-style format used by OpenSSL, including:
//   - Comment lines starting with # or ;
//   - Section headers in [brackets]
//   - Key = value pairs with optional quotes
//   - Multi-line values with backslash continuation
//
// Search paths are checked in order of precedence, with environment variable
// OPENSSL_CONF taking priority, followed by common OS-specific locations.
package system

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// opensslConfigPaths defines the search paths for OpenSSL configuration files.
// The paths are checked in order, with the first existing file being used.
// OPENSSL_CONF environment variable takes priority when set.
var opensslConfigPaths = []string{
	"/etc/ssl/openssl.cnf",               // Debian/Ubuntu
	"/etc/pki/tls/openssl.cnf",           // RHEL/CentOS/Fedora
	"/usr/local/etc/openssl/openssl.cnf", // macOS Homebrew
	"/usr/local/ssl/openssl.cnf",         // OpenSSL default
	"/usr/lib/ssl/openssl.cnf",           // Alternative Linux
	"/etc/openssl/openssl.cnf",           // Alpine
}

// OpenSSLConfig contains parsed default values from the OpenSSL configuration
// file's [req_distinguished_name] section.
//
// These values are extracted from the *_default fields in the OpenSSL config:
//   - countryName_default -> Country
//   - stateOrProvinceName_default -> Province
//   - localityName_default -> Locality
//   - 0.organizationName_default -> Organization
//   - organizationalUnitName_default -> OrganizationalUnit
//   - commonName_default -> CommonName
type OpenSSLConfig struct {
	// Country is the default country code (2-letter ISO 3166-1 alpha-2).
	Country string

	// Province is the default state or province name.
	Province string

	// Locality is the default city or locality name.
	Locality string

	// Organization is the default organization name.
	Organization string

	// OrganizationalUnit is the default organizational unit name.
	OrganizationalUnit string

	// CommonName is the default common name, if specified.
	CommonName string
}

// LoadOpenSSLConfig finds and parses the OpenSSL configuration file.
//
// This function searches for the OpenSSL config file in standard locations,
// giving priority to the OPENSSL_CONF environment variable. Once found,
// it parses the [req_distinguished_name] section to extract default values.
//
// Returns nil with no error if no OpenSSL config file is found.
// Returns an error only if a config file is found but cannot be parsed.
func LoadOpenSSLConfig() (*OpenSSLConfig, error) {
	path := findOpenSSLConfig()
	if path == "" {
		return nil, nil
	}
	return parseOpenSSLConfig(path)
}

// findOpenSSLConfig searches for the OpenSSL configuration file.
//
// The search order is:
//  1. OPENSSL_CONF environment variable (if set and file exists)
//  2. Standard OS-specific locations in opensslConfigPaths
//
// Returns the path to the first existing configuration file found,
// or an empty string if no configuration file is found.
func findOpenSSLConfig() string {
	// Check environment variable first.
	//
	// This is the repository's only flow from an environment variable to a
	// filesystem path, and the only G703 (path-traversal taint) site gosec
	// reports anywhere in the tree. The suppression is annotated HERE rather
	// than excluded repo-wide (#181) so a second such flow, added anywhere,
	// fails the scan instead of inheriting this justification:
	//
	//   - the taint source is OPENSSL_CONF, the documented OpenSSL operator
	//     mechanism, never a request parameter. Setting it requires control of
	//     the process environment, which already implies code execution as this
	//     user -- true even though LoadOpenSSLConfig is re-exported through
	//     sdk/go/types_system.go, since a downstream caller still reads ITS OWN
	//     process environment;
	//   - the path is only os.Stat'd here and os.Open'd read-only in
	//     parseOpenSSLConfig, then scanned. This file contains no
	//     Create/WriteFile/OpenFile/Remove/Rename/Chmod/Mkdir and no
	//     exec.Command.
	//
	// If either stops holding, this annotation is wrong and the finding is
	// real: fix the flow rather than widening the suppression.
	if envPath := os.Getenv("OPENSSL_CONF"); envPath != "" {
		cleanPath := filepath.Clean(envPath)
		if _, err := os.Stat(cleanPath); err == nil { // #nosec G703 -- taint source is the operator's process environment; read-only sink
			return cleanPath
		}
	}

	// Check standard paths
	for _, path := range opensslConfigPaths {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	return ""
}

// parseOpenSSLConfig parses an OpenSSL configuration file and extracts
// default Subject values from the [req_distinguished_name] section.
//
// The parser handles:
//   - Comment lines starting with # or ;
//   - Section headers in [brackets]
//   - Key = value pairs with optional surrounding whitespace
//   - Quoted values (both single and double quotes are stripped)
//   - Multi-line values with backslash continuation
//   - Variable references like $dir or ${ENV::VAR} (passed through as-is)
//
// Returns an OpenSSLConfig with the extracted default values.
// Returns an error if the file cannot be opened or read.
func parseOpenSSLConfig(path string) (*OpenSSLConfig, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	cfg := &OpenSSLConfig{}
	scanner := bufio.NewScanner(file)

	var currentSection string
	var continuationKey string
	var continuationValue strings.Builder

	// Field mapping from OpenSSL config keys to struct fields
	fieldMap := map[string]*string{
		"countryname_default":            &cfg.Country,
		"stateorprovincename_default":    &cfg.Province,
		"localityname_default":           &cfg.Locality,
		"0.organizationname_default":     &cfg.Organization,
		"organizationalunitname_default": &cfg.OrganizationalUnit,
		"commonname_default":             &cfg.CommonName,
	}

	for scanner.Scan() {
		line := scanner.Text()

		// Handle line continuation
		if continuationKey != "" {
			trimmed := strings.TrimSpace(line)
			if strings.HasSuffix(trimmed, "\\") {
				continuationValue.WriteString(strings.TrimSuffix(trimmed, "\\"))
				continue
			}
			continuationValue.WriteString(trimmed)

			// Apply the continued value
			if currentSection == "req_distinguished_name" {
				normalizedKey := strings.ToLower(strings.TrimSpace(continuationKey))
				if field, ok := fieldMap[normalizedKey]; ok {
					*field = stripQuotes(continuationValue.String())
				}
			}
			continuationKey = ""
			continuationValue.Reset()
			continue
		}

		// Skip empty lines
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Skip comment lines
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}

		// Check for section header
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			currentSection = strings.TrimSpace(line[1 : len(line)-1])
			continue
		}

		// Only process req_distinguished_name section
		if currentSection != "req_distinguished_name" {
			continue
		}

		// Parse key = value pair
		key, value, found := parseKeyValue(line)
		if !found {
			continue
		}

		// Check for line continuation
		if strings.HasSuffix(value, "\\") {
			continuationKey = key
			continuationValue.WriteString(strings.TrimSuffix(value, "\\"))
			continue
		}

		// Apply the value to the appropriate field
		normalizedKey := strings.ToLower(strings.TrimSpace(key))
		if field, ok := fieldMap[normalizedKey]; ok {
			*field = stripQuotes(value)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// parseKeyValue parses a line in the format "key = value" or "key=value".
//
// Returns the key, value, and a boolean indicating if parsing succeeded.
// Returns empty strings and false for lines that don't contain a valid
// key-value pair.
func parseKeyValue(line string) (key, value string, ok bool) {
	// Find the first equals sign
	idx := strings.Index(line, "=")
	if idx == -1 {
		return "", "", false
	}

	key = strings.TrimSpace(line[:idx])
	value = strings.TrimSpace(line[idx+1:])

	// Handle inline comments (remove everything after unquoted # or ;)
	value = removeInlineComment(value)

	return key, value, true
}

// removeInlineComment removes inline comments from a value string.
//
// Comments start with # or ; but only when not inside quotes.
// Returns the value with any inline comment removed.
func removeInlineComment(value string) string {
	inSingleQuote := false
	inDoubleQuote := false

	for i, ch := range value {
		switch ch {
		case '\'':
			if !inDoubleQuote {
				inSingleQuote = !inSingleQuote
			}
		case '"':
			if !inSingleQuote {
				inDoubleQuote = !inDoubleQuote
			}
		case '#', ';':
			if !inSingleQuote && !inDoubleQuote {
				return strings.TrimSpace(value[:i])
			}
		}
	}

	return value
}

// stripQuotes removes surrounding single or double quotes from a string.
//
// Only removes quotes if the string starts and ends with the same quote character.
// Returns the original string if no matching quotes are found.
func stripQuotes(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 2 {
		return s
	}

	// Check for matching quotes
	if (s[0] == '"' && s[len(s)-1] == '"') ||
		(s[0] == '\'' && s[len(s)-1] == '\'') {
		return s[1 : len(s)-1]
	}

	return s
}
