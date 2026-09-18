package mcp

// Based on the MCP specification: https://modelcontextprotocol.io/spec/

// ParameterDetail describes a single parameter for an operation.
type ParameterDetail struct {
	Name string `json:"name"`
	In   string `json:"in"` // Location (query, header, path, cookie)
	// Add other details if needed, e.g., required, type
}

// OperationDetail holds the necessary information to execute a specific API operation.
type OperationDetail struct {
	Method     string            `json:"method"`
	Path       string            `json:"path"` // Path template (e.g., /users/{id})
	BaseURL    string            `json:"baseUrl"`
	Parameters []ParameterDetail `json:"parameters,omitempty"`
	// Add RequestBody schema if needed
}

// ToolSet represents the collection of tools provided by an MCP server.
type ToolSet struct {
	MCPVersion  string `json:"mcp_version"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Auth        *AuthInfo `json:"auth,omitempty"` // Removed authentication info
	Tools []Tool `json:"tools"`

	// Security holds the security schemes declared by the spec (from
	// components.securitySchemes / securityDefinitions). Used to infer the API's
	// auth config (see server.InferAuthConfig). Internal, not part of the MCP
	// tool JSON.
	Security []SecurityScheme `json:"-"`

	// BaseDir is the directory of the source spec file, used to resolve external
	// $ref files (e.g. "schemas.yaml#/..."). Internal, not serialized.
	BaseDir string `json:"-"`

	// BaseURL is the URL of the source spec when it was loaded from http(s),
	// used to resolve external $ref files served alongside it. Internal.
	BaseURL string `json:"-"`

	// Operations maps Tool.Name (operationId) to its execution details.
	// This is internal to the server and not part of the standard MCP JSON response.
	Operations map[string]OperationDetail `json:"-"` // Use json:"-" to exclude from JSON

	// Internal fields for server-side auth handling (not exposed in JSON)
	apiKeyName string // e.g., "key", "X-API-Key"
	apiKeyIn   string // e.g., "query", "header"
}

// SecurityScheme is a normalized, spec-level security scheme. It carries the
// authentication *type* and, for apiKey, where the key parameter lives. It does
// not carry actual secrets.
type SecurityScheme struct {
	Key          string // key in components.securitySchemes / securityDefinitions
	Type         string // apiKey | http | oauth2 | openIdConnect | mutualTLS | basic (v2)
	In           string // apiKey: header | query | cookie
	Name         string // apiKey: parameter name
	Scheme       string // http: basic | bearer | digest
	BearerFormat string
	Flow         string // oauth2: password | clientCredentials | authorizationCode | implicit
	TokenURL     string // oauth2 token URL
	AuthURL      string // oauth2 authorization URL
	Required     bool   // referenced by the root security requirement
}

// SetAPIKeyDetails allows the parser to set internal API key info.
func (ts *ToolSet) SetAPIKeyDetails(name, in string) {
	ts.apiKeyName = name
	ts.apiKeyIn = in
}

// GetAPIKeyDetails allows the server to retrieve internal API key info.
// We might need this later when making the request.
func (ts *ToolSet) GetAPIKeyDetails() (name, in string) {
	return ts.apiKeyName, ts.apiKeyIn
}

// Tool represents a single function or capability exposed via MCP.
type Tool struct {
	Name        string `json:"name"` // Corresponds to OpenAPI operationId or generated name
	Description string `json:"description,omitempty"`
	InputSchema Schema `json:"inputSchema"` // Renamed from Parameters, consolidate parameters/body here
	// Entrypoint  string      `json:"entrypoint"`             // Removed for simplicity, schema should contain enough info?
	// RequestBody RequestBody `json:"request_body,omitempty"` // Removed, info should be part of InputSchema
	// HTTPMethod  string      `json:"http_method"`            // Removed for simplicity
	// TODO: Add Response handling if needed by spec/client
}

// RequestBody describes the expected request body for a tool.
// This might become redundant if all info is in InputSchema.
// Keeping it for now as the parser might still use it internally.
type RequestBody struct {
	Description string            `json:"description,omitempty"`
	Required    bool              `json:"required,omitempty"`
	Content     map[string]Schema `json:"content"` // Keyed by media type (e.g., "application/json")
}

// Schema defines the structure and constraints of data (parameters or request/response bodies).
// This mirrors a subset of JSON Schema properties.
type Schema struct {
	Type        string            `json:"type,omitempty"` // e.g., "object", "string", "integer", "array"
	Description string            `json:"description,omitempty"`
	Properties  map[string]Schema `json:"properties,omitempty"` // For type "object"
	Required    []string          `json:"required,omitempty"`   // For type "object"
	Items       *Schema           `json:"items,omitempty"`      // For type "array"
	Format      string            `json:"format,omitempty"`     // e.g., "int32", "date-time"
	Enum        []interface{}     `json:"enum,omitempty"`
	// Add other relevant JSON Schema fields as needed (e.g., minimum, maximum, pattern)
}

// --- API introspection / documentation model ---
//
// ApiDoc is a normalized, client-friendly description of a registered API
// derived from the OpenAPI/Swagger spec. It powers the introspection
// management tools (get_api_info, list_api_endpoints, get_api_operation,
// list_api_schemas) so AI agents can understand an API and how to use it
// without reading the raw spec.

// ApiDoc is the top-level documentation of an API.
type ApiDoc struct {
	Title       string                `json:"title,omitempty"`
	Version     string                `json:"version,omitempty"`
	Description string                `json:"description,omitempty"`
	Servers     []string              `json:"servers,omitempty"`
	Tags        []string              `json:"tags,omitempty"`
	Endpoints   []EndpointDoc         `json:"endpoints,omitempty"`
	Schemas     map[string]*SchemaDoc `json:"schemas,omitempty"` // DTOs/components
}

// EndpointDoc describes a single operation (endpoint).
type EndpointDoc struct {
	OperationID string         `json:"operation_id,omitempty"`
	ToolName    string         `json:"tool_name,omitempty"` // fully qualified MCP tool name (<api>__<op>), filled by the registry
	Summary     string         `json:"summary,omitempty"`
	Description string         `json:"description,omitempty"`
	Method      string         `json:"method"`
	Path        string         `json:"path"`
	Tags        []string       `json:"tags,omitempty"`
	Parameters  []ParameterDoc `json:"parameters,omitempty"`
	RequestBody *SchemaDoc     `json:"request_body,omitempty"`
	Responses   []ResponseDoc  `json:"responses,omitempty"`
}

// ParameterDoc describes a single operation parameter.
type ParameterDoc struct {
	Name        string     `json:"name"`
	In          string     `json:"in"` // query, header, path, cookie, formData
	Required    bool       `json:"required,omitempty"`
	Description string     `json:"description,omitempty"`
	Schema      *SchemaDoc `json:"schema,omitempty"`
}

// ResponseDoc describes one response of an operation.
type ResponseDoc struct {
	Status      string     `json:"status"` // e.g. "200", "4XX"
	Description string     `json:"description,omitempty"`
	Schema      *SchemaDoc `json:"schema,omitempty"`
}

// SchemaDoc is a JSON-Schema-ish description of a DTO or inline schema.
type SchemaDoc struct {
	Ref         string                `json:"ref,omitempty"`  // $ref target, if the schema was a reference
	Name        string                `json:"name,omitempty"` // component/definition name if resolvable
	Type        string                `json:"type,omitempty"` // object/string/... (emptied for references)
	Description string                `json:"description,omitempty"`
	Format      string                `json:"format,omitempty"`
	Enum        []interface{}         `json:"enum,omitempty"`
	Properties  map[string]*SchemaDoc `json:"properties,omitempty"` // for type object
	Required    []string              `json:"required,omitempty"`
	Items       *SchemaDoc            `json:"items,omitempty"` // for type array
}
