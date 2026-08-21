package api

import _ "embed"

// OpenAPI is the canonical external AI Gateway API contract.
//
//go:embed openapi.yaml
var OpenAPI []byte
