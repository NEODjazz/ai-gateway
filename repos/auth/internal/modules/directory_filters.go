package modules

import (
	"strings"
)

// DirectoryFilter adds literal substring search and exact policy filters without
// changing the legacy directory/SCIM pagination and sort contracts.
type DirectoryFilter struct {
	Search string
	Status string
	Role   string
}

func directoryFilter(filters []DirectoryFilter) (DirectoryFilter, error) {
	if len(filters) > 1 {
		return DirectoryFilter{}, ErrInvalidDirectoryEntry
	}
	var filter DirectoryFilter
	if len(filters) == 1 {
		filter = filters[0]
	}
	filter.Search = strings.TrimSpace(filter.Search)
	filter.Status = strings.TrimSpace(filter.Status)
	filter.Role = strings.TrimSpace(filter.Role)
	if len(filter.Search) > 256 || strings.ContainsRune(filter.Search, 0) || len(filter.Role) > 128 || strings.ContainsAny(filter.Role, "\x00\r\n") || (filter.Status != "" && !validDirectoryStatus(filter.Status)) || (filter.Role != "" && !validPolicyStrings([]string{filter.Role})) {
		return DirectoryFilter{}, ErrInvalidDirectoryEntry
	}
	return filter, nil
}
