package tenant

import (
	"maps"
	"strconv"
	"unicode/utf8"
)

// Bounds on a tenant's descriptive fields, TenantInfo.Description and
// TenantInfo.Metadata. Sizes are in bytes of UTF-8. They bound the tenant
// record every node replicates and every transport carries; a patch outside
// them is refused with *TenantFieldError.
const (
	// MaxDescriptionBytes bounds TenantInfo.Description.
	MaxDescriptionBytes = 1024
	// MaxMetadataEntries bounds the number of TenantInfo.Metadata entries.
	MaxMetadataEntries = 64
	// MaxMetadataKeyBytes bounds one TenantInfo.Metadata key, the length of
	// a DNS label (RFC 1035 §2.3.4).
	MaxMetadataKeyBytes = 63
	// MaxMetadataValueBytes bounds one TenantInfo.Metadata value.
	MaxMetadataValueBytes = 256
)

// validateDescriptive checks the descriptive fields patch sets against the
// bounds above. A field the patch leaves unchanged (nil) is not checked.
func validateDescriptive(patch TenantPatch) error {
	if patch.Description != nil {
		if err := checkText("description", *patch.Description, MaxDescriptionBytes); err != nil {
			return err
		}
	}
	if patch.Metadata == nil {
		return nil
	}
	if len(patch.Metadata) > MaxMetadataEntries {
		return &TenantFieldError{Field: "metadata", Reason: "more than " + strconv.Itoa(MaxMetadataEntries) + " entries"}
	}
	for k, v := range patch.Metadata {
		if err := checkMetadataKey(k); err != nil {
			return err
		}
		if err := checkText("metadata value", v, MaxMetadataValueBytes); err != nil {
			return err
		}
	}
	return nil
}

// checkMetadataKey refuses a key outside the Kubernetes label-name grammar
// without a prefix: 1 to MaxMetadataKeyBytes bytes of [a-z0-9._-] that begin
// and end with [a-z0-9].
func checkMetadataKey(k string) error {
	if k == "" {
		return &TenantFieldError{Field: "metadata key", Reason: "empty"}
	}
	if len(k) > MaxMetadataKeyBytes {
		return &TenantFieldError{Field: "metadata key", Reason: "exceeds " + strconv.Itoa(MaxMetadataKeyBytes) + " bytes"}
	}
	if !isKeyAlnum(k[0]) || !isKeyAlnum(k[len(k)-1]) {
		return &TenantFieldError{Field: "metadata key", Reason: "must begin and end with [a-z0-9]"}
	}
	for i := 1; i < len(k)-1; i++ {
		if c := k[i]; !isKeyAlnum(c) && c != '.' && c != '_' && c != '-' {
			return &TenantFieldError{Field: "metadata key", Reason: "must contain only [a-z0-9._-]"}
		}
	}
	return nil
}

// isKeyAlnum reports whether c is a lowercase ASCII letter or a digit.
func isKeyAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
}

// checkText refuses s when it is longer than limit bytes or not valid UTF-8.
func checkText(field, s string, limit int) error {
	if len(s) > limit {
		return &TenantFieldError{Field: field, Reason: "exceeds " + strconv.Itoa(limit) + " bytes"}
	}
	if !utf8.ValidString(s) {
		return &TenantFieldError{Field: field, Reason: "not valid UTF-8"}
	}
	return nil
}

// applyDescriptive writes the descriptive fields patch sets onto info. A set
// Description replaces the old one ("" clears it); a non-nil Metadata
// replaces the whole map, and an empty one clears it. The map is copied, so
// the caller's patch never aliases the record.
func applyDescriptive(info *TenantInfo, patch TenantPatch) {
	if patch.Description != nil {
		info.Description = *patch.Description
	}
	if patch.Metadata == nil {
		return
	}
	if len(patch.Metadata) == 0 {
		info.Metadata = nil
		return
	}
	info.Metadata = maps.Clone(patch.Metadata)
}
