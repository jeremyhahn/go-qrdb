// Package wireconflict is the structured-refusal codec of a server-side
// command conflict: a write refused because it would violate a unique
// secondary index. The server (pkg/api/errclass) encodes it and the client
// (sdk/go/transport) decodes it; neither side exports the wire form, so the
// only public face of the refusal is a *dberrors.QRDBError with Code
// ErrConflict reached through errors.As (#1735).
package wireconflict

// Reason is the structured-refusal reason every transport carries beside the
// message of a server-side command conflict.
const Reason = "COMMAND_CONFLICT"

// keyMessage is the metadata key carrying the refusal message.
const keyMessage = "message"

// Metadata returns the structured-refusal metadata carrying message.
func Metadata(message string) map[string]string {
	return map[string]string{keyMessage: message}
}

// Message returns the refusal message Metadata encoded.
func Message(md map[string]string) string {
	return md[keyMessage]
}
