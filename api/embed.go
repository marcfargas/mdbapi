// Package api provides the embedded OpenAPI specification.
package api

import _ "embed"

//go:embed openapi.yaml
var OpenAPIYAML []byte
