// Package api embeds the public HTTP contract so the control plane can serve
// it at /api/v1/openapi.yaml.
package api

import _ "embed"

//go:embed openapi.yaml
var OpenAPI []byte
