package modules

import (
	"strings"
	"testing"
)

func TestDirectoryFilterValidation(t *testing.T) {
	for _, filter := range []DirectoryFilter{{Search: strings.Repeat("x", 257)}, {Search: "a\x00b"}, {Status: "all"}, {Role: strings.Repeat("x", 129)}, {Role: "admin\r\nother"}} {
		if _, err := directoryFilter([]DirectoryFilter{filter}); err == nil {
			t.Fatalf("accepted invalid filter: %+v", filter)
		}
	}
	filter, err := directoryFilter([]DirectoryFilter{{Search: "  %_owner  ", Status: "active", Role: "user"}})
	if err != nil || filter.Search != "%_owner" {
		t.Fatal("literal search normalization failed", err)
	}
}
