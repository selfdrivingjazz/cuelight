package api_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

func TestOpenAPIValidates(t *testing.T) {
	doc := loadOpenAPI(t)
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatalf("validate OpenAPI: %v", err)
	}
}

func TestOperatorOperationsDeclareCableScopes(t *testing.T) {
	doc := loadOpenAPI(t)
	for path, item := range doc.Paths.Map() {
		for method, operation := range item.Operations() {
			operator := false
			for _, tag := range operation.Tags {
				operator = operator || tag == "operator"
			}
			if !operator {
				continue
			}
			if _, ok := operation.Extensions["x-required-scopes"]; !ok {
				t.Errorf("%s %s has no x-required-scopes", method, path)
			}
		}
	}
}

func loadOpenAPI(t *testing.T) *openapi3.T {
	t.Helper()
	path, err := filepath.Abs("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = false
	doc, err := loader.LoadFromFile(path)
	if err != nil {
		t.Fatalf("load OpenAPI: %v", err)
	}
	return doc
}
