package parser

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ckanthony/openapi-mcp/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildApiDocExternalSchemasIndexed(t *testing.T) {
	dir := t.TempDir()
	ext := `components:
  schemas:
    LoginRequest:
      type: object
      required: [username, password]
      properties:
        username: { type: string }
        password: { type: string }
    Operator:
      type: object
      properties:
        id: { type: integer }
    LoginResponse:
      type: object
      properties:
        operator: { $ref: '#/components/schemas/Operator' }
        token: { type: string }
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "schemas.yaml"), []byte(ext), 0644))

	spec := `openapi: 3.0.0
info: { title: X, version: "1" }
paths:
  /auth/login:
    post:
      operationId: login
      requestBody:
        required: true
        content:
          application/json:
            schema: { $ref: 'schemas.yaml#/components/schemas/LoginRequest' }
      responses:
        '200':
          description: ok
          content:
            application/json:
              schema: { $ref: 'schemas.yaml#/components/schemas/LoginResponse' }
`
	specPath := filepath.Join(dir, "api.yaml")
	require.NoError(t, os.WriteFile(specPath, []byte(spec), 0644))

	doc, version, err := LoadSwagger(specPath)
	require.NoError(t, err)
	require.Equal(t, VersionV3, version)

	ts, err := GenerateToolSet(doc, version, &config.Config{})
	require.NoError(t, err)
	ts.BaseDir = dir
	apiDoc := BuildApiDoc(doc, version, ts)

	require.Contains(t, apiDoc.Schemas, "LoginRequest")
	require.Contains(t, apiDoc.Schemas, "LoginResponse")
	require.Contains(t, apiDoc.Schemas, "Operator")
	assert.Equal(t, "object", apiDoc.Schemas["LoginRequest"].Type)
	assert.Contains(t, apiDoc.Schemas["LoginRequest"].Required, "password")
	assert.Contains(t, apiDoc.Schemas["Operator"].Properties, "id")

	require.Len(t, apiDoc.Endpoints, 1)
	assert.Equal(t, "LoginRequest", apiDoc.Endpoints[0].RequestBody.Name)
	assert.Equal(t, "LoginResponse", apiDoc.Endpoints[0].Responses[0].Schema.Name)
}

func TestBuildApiDocExternalSchemasOverHTTP(t *testing.T) {
	// A spec served at a URL whose $ref points to a sibling file over the same
	// host (common when vendors host a downloadable OpenAPI spec).
	spec := `openapi: 3.0.0
info: { title: X, version: "1" }
paths:
  /login:
    post:
      operationId: login
      requestBody:
        required: true
        content:
          application/json:
            schema: { $ref: 'schemas.yaml#/components/schemas/LoginRequest' }
      responses:
        '200':
          description: ok
          content:
            application/json:
              schema: { $ref: 'schemas.yaml#/components/schemas/LoginResponse' }
`
	ext := `components:
  schemas:
    LoginRequest:
      type: object
      properties:
        username: { type: string }
    Operator:
      type: object
      properties:
        id: { type: integer }
    LoginResponse:
      type: object
      properties:
        operator: { $ref: '#/components/schemas/Operator' }
        token: { type: string }
`
	mux := http.NewServeMux()
	mux.HandleFunc("/openapi.json", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, spec) })
	mux.HandleFunc("/schemas.yaml", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, ext) })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	url := srv.URL + "/openapi.json"
	doc, version, err := LoadSwagger(url)
	require.NoError(t, err)
	require.Equal(t, VersionV3, version)

	ts, err := GenerateToolSet(doc, version, &config.Config{})
	require.NoError(t, err)
	ts.BaseURL = url
	apiDoc := BuildApiDoc(doc, version, ts)

	require.Contains(t, apiDoc.Schemas, "LoginRequest")
	require.Contains(t, apiDoc.Schemas, "LoginResponse")
	require.Contains(t, apiDoc.Schemas, "Operator")
	assert.Equal(t, "object", apiDoc.Schemas["LoginRequest"].Type)
	assert.Contains(t, apiDoc.Schemas["Operator"].Properties, "id")
}
