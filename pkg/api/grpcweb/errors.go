package grpcweb

import "strconv"

// WildcardCredentialsError is returned by Config.Validate when the "*"
// origin is combined with AllowCredentials. The Fetch standard forbids a
// credentialed response to a wildcard origin, and echoing every origin
// instead would admit credentialed calls from any site.
type WildcardCredentialsError struct{}

func (e *WildcardCredentialsError) Error() string {
	return "grpcweb: allow_credentials cannot be combined with the \"*\" origin"
}

// OriginError is returned by Config.Validate when an allowed-origins entry
// is neither "*" nor a serialized origin.
type OriginError struct {
	Origin string
}

func (e *OriginError) Error() string {
	return "grpcweb: allowed origin " + strconv.Quote(e.Origin) +
		" is not a serialized origin (scheme://host[:port])"
}
