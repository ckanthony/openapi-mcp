package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ckanthony/openapi-mcp/pkg/config"
	"github.com/ckanthony/openapi-mcp/pkg/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const introspectSpec = `{
  "openapi": "3.0.0",
  "info": {"title": "Orders API", "version": "2.3.0", "description": "Manage orders"},
  "servers": [{"url": "https://orders.example.com/v2"}],
  "tags": [{"name": "orders"}],
  "paths": {
    "/orders": {
      "get": {
        "summary": "List orders",
        "description": "Returns a page of orders",
        "operationId": "listOrders",
        "tags": ["orders"],
        "parameters": [
          {"name": "page", "in": "query", "required": false, "schema": {"type": "integer", "format": "int32"}},
          {"name": "X-Tenant", "in": "header", "required": true, "schema": {"type": "string"}}
        ],
        "responses": {
          "200": {"description": "OK", "content": {"application/json": {"schema": {"type": "array", "items": {"$ref": "#/components/schemas/Order"}}}}}
        }
      },
      "post": {
        "summary": "Create order",
        "operationId": "createOrder",
        "tags": ["orders"],
        "requestBody": {
          "required": true,
          "content": {"application/json": {"schema": {"$ref": "#/components/schemas/OrderInput"}}}
        },
        "responses": {
          "201": {"description": "Created", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Order"}}}}
        }
      }
    }
  },
  "components": {
    "schemas": {
      "Order": {
        "type": "object",
        "required": ["id"],
        "properties": {
          "id": {"type": "string"},
          "total": {"type": "number"}
        }
      },
      "OrderInput": {
        "type": "object",
        "properties": {
          "item": {"type": "string"}
        }
      }
    }
  }
}`

func TestToolDescriptionReflectsEndpoint(t *testing.T) {
	reg := NewRegistry("")
	reg.RegisterAPI(exposedAPI(config.APIDefinition{Name: "orders", Spec: introspectSpec}), false)

	// tools/list exposes the full (prefixed) name; the description must embed
	// the endpoint and the tool name (self-referencing).
	var listTool *mcp.Tool
	for _, t := range reg.Tools() {
		if t.Name == "orders__listOrders" {
			listTool = &t
			break
		}
	}
	require.NotNil(t, listTool)
	assert.Contains(t, listTool.Description, `Endpoint: GET "https://orders.example.com/v2/orders"`)
	assert.Contains(t, listTool.Description, "orders__listOrders")
	assert.Contains(t, listTool.Description, "X-Tenant (header, required): string")
	assert.Contains(t, listTool.Description, "page (query, optional): integer")
}

func TestGetAPIInfo(t *testing.T) {
	reg := NewRegistry("")
	reg.RegisterAPI(exposedAPI(config.APIDefinition{
		Name: "orders",
		Spec: introspectSpec,
		Auth: config.AuthConfig{Type: config.AuthAPIKey, In: "header", Name: "X-API-Key"},
		Targets: []config.TargetDefinition{
			{Name: "prod", BaseURL: "https://orders.example.com/v2", APIKey: "k"},
		},
		ActiveTarget: "prod",
	}), false)

	res := reg.runManagementTool("", ToolGetAPIInfo, map[string]interface{}{"api": "orders"})
	require.True(t, res.ok, res.text)

	var view struct {
		Info struct {
			Name    string   `json:"name"`
			Title   string   `json:"title"`
			Version string   `json:"version"`
			Servers []string `json:"servers"`
		} `json:"info"`
		Auth   config.AuthConfig `json:"auth"`
		Config struct {
			ToolCount int `json:"tool_count"`
		} `json:"config"`
		Targets []struct {
			Name    string `json:"name"`
			BaseURL string `json:"base_url"`
		} `json:"targets"`
		Active string `json:"active_target"`
	}
	require.NoError(t, json.Unmarshal([]byte(res.text), &view))
	assert.Equal(t, "Orders API", view.Info.Title)
	assert.Equal(t, "2.3.0", view.Info.Version)
	assert.Equal(t, "https://orders.example.com/v2", view.Info.Servers[0])
	assert.Equal(t, config.AuthAPIKey, view.Auth.Type)
	require.Len(t, view.Targets, 1)
	assert.Equal(t, "prod", view.Targets[0].Name)
	assert.Equal(t, "prod", view.Active)
	assert.Equal(t, 2, view.Config.ToolCount)
}

func TestListAPIEndpoints(t *testing.T) {
	reg := NewRegistry("")
	reg.RegisterAPI(exposedAPI(config.APIDefinition{Name: "orders", Spec: introspectSpec}), false)

	res := reg.runManagementTool("", ToolListEndpoints, map[string]interface{}{"api": "orders"})
	require.True(t, res.ok, res.text)
	var v struct {
		Endpoints []endpointView `json:"endpoints"`
		Total     int            `json:"total"`
	}
	require.NoError(t, json.Unmarshal([]byte(res.text), &v))
	require.Len(t, v.Endpoints, 2)
	assert.Equal(t, 2, v.Total)

	// Endpoint->tool mapping (self-referencing at the REST + tool levels).
	var listEp, createEp *endpointView
	for i := range v.Endpoints {
		if v.Endpoints[i].OperationID == "listOrders" {
			listEp = &v.Endpoints[i]
		}
		if v.Endpoints[i].OperationID == "createOrder" {
			createEp = &v.Endpoints[i]
		}
	}
	require.NotNil(t, listEp)
	require.NotNil(t, createEp)
	assert.Equal(t, "orders__listOrders", listEp.ToolName)
	assert.Equal(t, "orders__createOrder", createEp.ToolName)
	assert.Equal(t, "GET", listEp.Method)
	assert.Equal(t, "/orders", listEp.Path)

	// Every endpoint's tool name must actually exist in tools/list.
	names := map[string]bool{}
	for _, tt := range reg.Tools() {
		names[tt.Name] = true
	}
	for _, ep := range v.Endpoints {
		assert.True(t, names[ep.ToolName], "endpoint %s references tool %s which must exist", ep.OperationID, ep.ToolName)
	}
}

func TestGetApiOperation(t *testing.T) {
	reg := NewRegistry("")
	reg.RegisterAPI(exposedAPI(config.APIDefinition{Name: "orders", Spec: introspectSpec}), false)

	// By bare operationId.
	res := reg.runManagementTool("", ToolGetOperation, map[string]interface{}{"api": "orders", "operation": "createOrder"})
	require.True(t, res.ok, res.text)

	var op struct {
		OperationID string             `json:"operation_id"`
		ToolName    string             `json:"tool_name"`
		Method      string             `json:"method"`
		Path        string             `json:"path"`
		Parameters  []mcp.ParameterDoc `json:"parameters"`
		RequestBody *mcp.SchemaDoc     `json:"request_body"`
		Responses   []mcp.ResponseDoc  `json:"responses"`
	}
	require.NoError(t, json.Unmarshal([]byte(res.text), &op))
	assert.Equal(t, "orders__createOrder", op.ToolName)
	assert.Equal(t, "POST", op.Method)
	assert.Equal(t, "/orders", op.Path)
	require.NotNil(t, op.RequestBody)
	assert.Equal(t, "OrderInput", op.RequestBody.Name)
	require.Len(t, op.Responses, 1)
	assert.Equal(t, "201", op.Responses[0].Status)

	// By full tool name.
	res = reg.runManagementTool("", ToolGetOperation, map[string]interface{}{"api": "orders", "operation": "orders__listOrders"})
	require.True(t, res.ok, res.text)
	var op2 struct {
		OperationID string `json:"operation_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(res.text), &op2))
	assert.Equal(t, "listOrders", op2.OperationID)

	// Unknown operation -> error.
	res = reg.runManagementTool("", ToolGetOperation, map[string]interface{}{"api": "orders", "operation": "nope"})
	assert.False(t, res.ok)
	assert.Contains(t, res.text, "no operation")
}

func TestListAPISchemas(t *testing.T) {
	reg := NewRegistry("")
	reg.RegisterAPI(exposedAPI(config.APIDefinition{Name: "orders", Spec: introspectSpec}), false)

	// Compact list.
	res := reg.runManagementTool("", ToolListSchemas, map[string]interface{}{"api": "orders"})
	require.True(t, res.ok, res.text)
	var compact map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(res.text), &compact))
	schemas, _ := compact["schemas"].(map[string]interface{})
	assert.Contains(t, schemas, "Order")
	assert.Equal(t, float64(2), compact["total"])

	// Single schema by name (full expansion).
	res = reg.runManagementTool("", ToolListSchemas, map[string]interface{}{"api": "orders", "name": "Order"})
	require.True(t, res.ok, res.text)
	var order mcp.SchemaDoc
	require.NoError(t, json.Unmarshal([]byte(res.text), &order))
	assert.Equal(t, "object", order.Type)
	assert.Contains(t, order.Required, "id")
	assert.Contains(t, order.Properties, "id")
	assert.Contains(t, order.Properties, "total")
}

func TestIntrospectionV2Spec(t *testing.T) {
	// Swagger 2.0: definitions + endpoints must be introspectable.
	const v2spec = `{
	  "swagger": "2.0",
	  "info": {"title": "Legacy API", "version": "1.0"},
	  "host": "legacy.example.com",
	  "basePath": "/v1",
	  "schemes": ["https"],
	  "definitions": {
	    "Item": {
	      "type": "object",
	      "required": ["id"],
	      "properties": {
	        "id": {"type": "string"},
	        "qty": {"type": "integer", "format": "int32"}
	      }
	    }
	  },
	  "paths": {
	    "/items": {
	      "get": {
	        "summary": "List items",
	        "operationId": "listItems",
	        "parameters": [
	          {"name": "limit", "in": "query", "type": "integer", "required": false}
	        ],
	        "responses": {
	          "200": {"description": "OK", "schema": {"$ref": "#/definitions/Item"}}
	        }
	      }
	    }
	  }
	}`
	reg := NewRegistry("")
	reg.RegisterAPI(exposedAPI(config.APIDefinition{Name: "legacy", Spec: v2spec}), false)

	res := reg.runManagementTool("", ToolGetAPIInfo, map[string]interface{}{"api": "legacy"})
	require.True(t, res.ok, res.text)
	var info struct {
		Info struct {
			Title   string   `json:"title"`
			Servers []string `json:"servers"`
		} `json:"info"`
	}
	require.NoError(t, json.Unmarshal([]byte(res.text), &info))
	assert.Equal(t, "Legacy API", info.Info.Title)
	assert.Contains(t, info.Info.Servers, "https://legacy.example.com/v1")

	res = reg.runManagementTool("", ToolListSchemas, map[string]interface{}{"api": "legacy"})
	require.True(t, res.ok, res.text)
	var schemas map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(res.text), &schemas))
	assert.Contains(t, schemas["schemas"].(map[string]interface{}), "Item")

	res = reg.runManagementTool("", ToolGetOperation, map[string]interface{}{"api": "legacy", "operation": "listItems"})
	require.True(t, res.ok, res.text)
	var op struct {
		ToolName   string             `json:"tool_name"`
		Method     string             `json:"method"`
		Path       string             `json:"path"`
		Parameters []mcp.ParameterDoc `json:"parameters"`
		Responses  []mcp.ResponseDoc  `json:"responses"`
	}
	require.NoError(t, json.Unmarshal([]byte(res.text), &op))
	assert.Equal(t, "legacy__listItems", op.ToolName)
	assert.Equal(t, "GET", op.Method)
	assert.Equal(t, "/items", op.Path)
	require.Len(t, op.Parameters, 1)
	assert.Equal(t, "limit", op.Parameters[0].Name)
	assert.Equal(t, "query", op.Parameters[0].In)
	require.Len(t, op.Responses, 1)
	assert.Equal(t, "200", op.Responses[0].Status)
	assert.Equal(t, "Item", op.Responses[0].Schema.Name)

	// The exposed tool description is reflective too.
	var listTool *mcp.Tool
	for _, tt := range reg.Tools() {
		if tt.Name == "legacy__listItems" {
			listTool = &tt
			break
		}
	}
	require.NotNil(t, listTool)
	assert.Contains(t, listTool.Description, `Endpoint: GET "https://legacy.example.com/v1/items"`)
	assert.Contains(t, listTool.Description, "limit (query, optional): integer")
}

func TestIntrospectionThroughHTTPHandler(t *testing.T) {
	// Exercise the introspection tools end-to-end through the MCP POST path.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{}`)
	}))
	defer backend.Close()

	toolSet := createTestToolSetForCall()
	reg := newTestRegistryForToolSet(toolSet, &config.Config{ServerBaseURL: backend.URL})
	_ = reg

	// The management tools are present in tools/list.
	names := toolNames(reg.Tools())
	assert.Contains(t, names, ToolGetAPIInfo)
	assert.Contains(t, names, ToolListEndpoints)
	assert.Contains(t, names, ToolGetOperation)
	assert.Contains(t, names, ToolListSchemas)
}

func TestIntrospectionExternalRefsNoPanic(t *testing.T) {
	// Schemas whose properties reference components (even when a ref's .Value is
	// unresolved) must not panic (nil seen map / nil schema).
	const extSpec = `{
	  "openapi": "3.0.0",
	  "info": {"title": "X", "version": "1"},
	  "paths": {
	    "/a": {"get": {"operationId": "getA",
	      "responses": {"200": {"description": "OK", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Wrapper"}}}}}}}
	  },
	  "components": {"schemas": {
	    "Wrapper": {"type": "object", "properties": {"items": {"type": "array", "items": {"$ref": "#/components/schemas/Inner"}}}},
	    "Inner": {"type": "object", "properties": {"id": {"type": "string"}}}
	  }}
	}`
	reg := NewRegistry("")
	reg.RegisterAPI(exposedAPI(config.APIDefinition{Name: "ext", Spec: extSpec}), false)
	res := reg.runManagementTool("", ToolListSchemas, map[string]interface{}{"api": "ext"})
	require.True(t, res.ok, res.text)
	var schemas map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(res.text), &schemas))
	ss := schemas["schemas"].(map[string]interface{})
	assert.Contains(t, ss, "Inner")
	assert.Contains(t, ss, "Wrapper")
}

func TestSearchOperations(t *testing.T) {
	reg := NewRegistry("")
	reg.RegisterAPI(exposedAPI(config.APIDefinition{Name: "orders", Spec: introspectSpec}), false)
	res := reg.runManagementTool("", ToolSearchOperation, map[string]interface{}{"api": "orders", "query": "create"})
	require.True(t, res.ok, res.text)
	var v struct {
		Matches []endpointView `json:"matches"`
	}
	require.NoError(t, json.Unmarshal([]byte(res.text), &v))
	require.Len(t, v.Matches, 1)
	assert.Equal(t, "orders__createOrder", v.Matches[0].ToolName)

	// Method filter.
	res = reg.runManagementTool("", ToolSearchOperation, map[string]interface{}{"api": "orders", "query": "", "method": "GET"})
	require.True(t, res.ok, res.text)
	var v2 struct {
		Matches []endpointView `json:"matches"`
	}
	require.NoError(t, json.Unmarshal([]byte(res.text), &v2))
	assert.Len(t, v2.Matches, 1)
}

func TestListEndpointsFilters(t *testing.T) {
	reg := NewRegistry("")
	reg.RegisterAPI(exposedAPI(config.APIDefinition{Name: "orders", Spec: introspectSpec}), false)

	// search narrows by operationId/path/summary substring.
	res := reg.runManagementTool("", ToolListEndpoints, map[string]interface{}{"api": "orders", "search": "create"})
	require.True(t, res.ok, res.text)
	var v struct {
		Total     int            `json:"total"`
		Endpoints []endpointView `json:"endpoints"`
	}
	require.NoError(t, json.Unmarshal([]byte(res.text), &v))
	assert.Equal(t, 1, v.Total)
	assert.Equal(t, "orders__createOrder", v.Endpoints[0].ToolName)

	// method filter.
	res = reg.runManagementTool("", ToolListEndpoints, map[string]interface{}{"api": "orders", "method": "GET"})
	require.True(t, res.ok, res.text)
	require.NoError(t, json.Unmarshal([]byte(res.text), &v))
	assert.Equal(t, 1, v.Total)
	assert.Equal(t, "GET", v.Endpoints[0].Method)

	// No matches is not an error.
	res = reg.runManagementTool("", ToolListEndpoints, map[string]interface{}{"api": "orders", "search": "zzz"})
	require.True(t, res.ok, res.text)
	require.NoError(t, json.Unmarshal([]byte(res.text), &v))
	assert.Equal(t, 0, v.Total)
}

func TestExportConfig(t *testing.T) {
	reg := NewRegistry("")
	reg.RegisterAPI(exposedAPI(config.APIDefinition{
		Name: "orders", Spec: introspectSpec,
		ActiveTarget: "prod",
		Targets:      []config.TargetDefinition{{Name: "prod", BaseURL: "https://prod.example.com"}},
	}), false)
	res := reg.runManagementTool("", ToolExportConfig, map[string]interface{}{"api": "orders"})
	require.True(t, res.ok, res.text)
	assert.Contains(t, res.text, `"name": "orders"`)
	assert.Contains(t, res.text, `"base_url": "https://prod.example.com"`)
}

func TestTestAPITarget(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	reg := NewRegistry("")
	reg.RegisterAPI(exposedAPI(config.APIDefinition{
		Name: "x", Spec: registryTestV3Spec,
		Targets: []config.TargetDefinition{{Name: "local", BaseURL: backend.URL}},
	}), false)
	res := reg.runManagementTool("", ToolTestTarget, map[string]interface{}{"api": "x", "target": "local"})
	require.True(t, res.ok, res.text)
	assert.Contains(t, res.text, "reachable")

	// Unknown target.
	res = reg.runManagementTool("", ToolTestTarget, map[string]interface{}{"api": "x", "target": "nope"})
	assert.False(t, res.ok)
}

func TestPreviewAPICall(t *testing.T) {
	reg := NewRegistry("")
	reg.RegisterAPI(exposedAPI(config.APIDefinition{
		Name: "orders", Spec: introspectSpec,
		Targets: []config.TargetDefinition{{Name: "local", BaseURL: "https://orders.example.com/v2"}},
	}), false)
	res := reg.runManagementTool("", ToolPreviewCall, map[string]interface{}{
		"operation": "orders__listOrders",
		"arguments": map[string]interface{}{"page": "2", "target": "local"},
	})
	require.True(t, res.ok, res.text)
	assert.Contains(t, res.text, `"method": "GET"`)
	assert.Contains(t, res.text, "Dry-run")
}

func TestCallAPIEndpoint(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/orders" {
			if r.Method == http.MethodPost {
				var in struct {
					Item string `json:"item"`
				}
				_ = json.NewDecoder(r.Body).Decode(&in)
				fmt.Fprintf(w, `{"created":"%s"}`, in.Item)
				return
			}
			fmt.Fprintf(w, `{"orders":[],"page":%q}`, r.URL.Query().Get("page"))
			return
		}
		w.WriteHeader(404)
		fmt.Fprint(w, `{"error":"not found"}`)
	}))
	defer backend.Close()

	reg := NewRegistry("")
	// No exposure configured: mode none, zero ops exposed. call_api_endpoint
	// must call the operation anyway (bypasses the exposure gate).
	_, err := reg.RegisterAPI(config.APIDefinition{
		Name: "orders", Spec: introspectSpec,
		Targets: []config.TargetDefinition{{Name: "local", BaseURL: backend.URL}},
	}, false)
	require.NoError(t, err)
	assert.NotContains(t, toolNames(reg.Tools()), "orders__listOrders")

	// By bare operationId.
	res := reg.runManagementTool("", ToolCallEndpoint, map[string]interface{}{
		"api": "orders", "operation": "listOrders",
		"arguments": map[string]interface{}{"page": "2", "target": "local"},
	})
	require.True(t, res.ok, res.text)
	assert.Contains(t, res.text, `"page":"2"`)

	// By full tool name with a body argument.
	res = reg.runManagementTool("", ToolCallEndpoint, map[string]interface{}{
		"api": "orders", "operation": "orders__createOrder",
		"arguments": map[string]interface{}{"item": "chair", "target": "local"},
	})
	require.True(t, res.ok, res.text)
	assert.Contains(t, res.text, `"created":"chair"`)

	// Unknown operation -> error.
	res = reg.runManagementTool("", ToolCallEndpoint, map[string]interface{}{"api": "orders", "operation": "nopeOp"})
	assert.False(t, res.ok)
	assert.Contains(t, res.text, "no operation")

	// Unknown API -> error.
	res = reg.runManagementTool("", ToolCallEndpoint, map[string]interface{}{"api": "nope", "operation": "listOrders"})
	assert.False(t, res.ok)

	// Full name whose API does not match the api argument -> error.
	res = reg.runManagementTool("", ToolCallEndpoint, map[string]interface{}{"api": "other", "operation": "orders__listOrders"})
	assert.False(t, res.ok)
	assert.Contains(t, res.text, "belongs to API")
}

func TestCallAPIEndpointRespectsAllowSet(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{}`)
	}))
	defer backend.Close()

	// The operator excluded getArchive via config: the allow-set is a HARD
	// boundary, so call_api_endpoint must refuse it even though exposure is
	// bypassed. listOrders (not excluded) still works.
	reg := NewRegistry("")
	_, err := reg.RegisterAPI(config.APIDefinition{
		Name:       "orders",
		Spec:       introspectSpec,
		ExcludeOps: []string{"createOrder"},
		Targets:    []config.TargetDefinition{{Name: "local", BaseURL: backend.URL}},
	}, false)
	require.NoError(t, err)

	res := reg.runManagementTool("", ToolCallEndpoint, map[string]interface{}{
		"api": "orders", "operation": "createOrder", "arguments": map[string]interface{}{"target": "local"},
	})
	assert.False(t, res.ok)
	assert.Contains(t, res.text, "allow-set")

	res = reg.runManagementTool("", ToolCallEndpoint, map[string]interface{}{
		"api": "orders", "operation": "listOrders", "arguments": map[string]interface{}{"target": "local"},
	})
	require.True(t, res.ok, res.text)
}

func TestListOpenAPIsCompactByDefault(t *testing.T) {
	reg := NewRegistry("")
	reg.RegisterAPI(exposedAPI(config.APIDefinition{
		Name: "orders", Spec: introspectSpec,
		Targets: []config.TargetDefinition{{Name: "local", BaseURL: "https://orders.example.com/v2"}},
	}), false)

	// Default listing is a compact summary: metadata + counts, no tool list.
	res := reg.runManagementTool("", ToolListAPIs, map[string]interface{}{})
	require.True(t, res.ok, res.text)
	assert.NotContains(t, res.text, `"tools"`)
	var rows []map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(res.text), &rows))
	require.Len(t, rows, 1)
	assert.Equal(t, "orders", rows[0]["name"])
	assert.EqualValues(t, 2, rows[0]["tool_count"])
	assert.Equal(t, "local", rows[0]["active_target"])

	// Opt in to the full tool names per API.
	res = reg.runManagementTool("", ToolListAPIs, map[string]interface{}{"include": []interface{}{"tools"}})
	require.True(t, res.ok, res.text)
	require.NoError(t, json.Unmarshal([]byte(res.text), &rows))
	tools, ok := rows[0]["tools"].([]interface{})
	require.True(t, ok, "include=[tools] must add the tool list")
	assert.Len(t, tools, 2)
	assert.Contains(t, tools, "orders__listOrders")
}
