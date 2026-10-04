package config

// ValidDocumentProcessing validates the gateway processing policy separately
// from the native capabilities advertised by a provider adapter.
func ValidDocumentProcessing(mode string) bool {
	return mode == "" || mode == "native" || mode == "docling"
}
