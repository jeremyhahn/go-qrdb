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
	// MaxMetadataKeyBytes bounds one TenantInfo.Metadata key.
	MaxMetadataKeyBytes = 128
	// MaxMetadataValueBytes bounds one TenantInfo.Metadata value.
	MaxMetadataValueBytes = 1024
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
		if k == "" {
			return &TenantFieldError{Field: "metadata key", Reason: "empty"}
		}
		if err := checkText("metadata key", k, MaxMetadataKeyBytes); err != nil {
			return err
		}
		if err := checkText("metadata value", v, MaxMetadataValueBytes); err != nil {
			return err
		}
	}
	return nil
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
