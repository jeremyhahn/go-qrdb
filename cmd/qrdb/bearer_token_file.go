package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// tokenFileSource is the transport.TokenSource behind the root flag
// --bearer-token-file. It reads the file on every request, so a token
// rotated in place (projected-token style) is picked up without a
// restart. Surrounding whitespace and a trailing newline are trimmed.
// An error names the path and the kind of failure only, never the file
// content; the SDK wraps it in *transport.TokenSourceError.
type tokenFileSource struct {
	path string
}

// Token returns the trimmed content of the token file, read now.
func (s tokenFileSource) Token(_ context.Context) (string, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return "", fmt.Errorf("qrdb: read bearer token file %s: %s", s.path, tokenFileErrKind(err))
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("qrdb: read bearer token file %s: empty", s.path)
	}
	return token, nil
}

// tokenFileErrKind names the kind of a token file read failure without
// echoing anything read from the file.
func tokenFileErrKind(err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "does not exist"
	case errors.Is(err, fs.ErrPermission):
		return "permission denied"
	default:
		var pathErr *fs.PathError
		if errors.As(err, &pathErr) {
			return pathErr.Err.Error()
		}
		return "unreadable"
	}
}

// checkTokenFileReadable refuses a --bearer-token-file that cannot be
// opened before any transport is built.
func checkTokenFileReadable(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return &InvalidArgError{Arg: "bearer-token-file", Reason: tokenFileErrKind(err)}
	}
	return f.Close()
}
