package parser

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ckanthony/openapi-mcp/pkg/config"
	"github.com/ckanthony/openapi-mcp/pkg/mcp"
	"github.com/ckanthony/openapi-mcp/pkg/utils"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/go-openapi/loads"
	"github.com/go-openapi/spec"
)

const (
	VersionV2 = "v2"
	VersionV3 = "v3"
)

// LoadSwagger detects the version and loads an OpenAPI/Swagger specification
// from a local file path or a remote URL.
// It returns the loaded spec document (as interface{}), the detected version (string), and an error.
func LoadSwagger(location string) (interface{}, string, error) {
	// Determine if location is URL or file path
	locationURL, urlErr := url.ParseRequestURI(location)
	isURL := urlErr == nil && locationURL != nil && (locationURL.Scheme == "http" || locationURL.Scheme == "https")

	var data []byte
	var err error
	var absPath string // Store absolute path if it's a file

	if !isURL {
		utils.SafeLogPrintf("Detected file path location: %s", location)
		absPath, err = filepath.Abs(location)
		if err != nil {
			return nil, "", fmt.Errorf("failed to get absolute path for '%s': %w", location, err)
		}
		// Read data first for version detection
		data, err = os.ReadFile(absPath)
		if err != nil {
			return nil, "", fmt.Errorf("failed reading file path '%s': %w", absPath, err)
		}
	} else {
		utils.SafeLogPrintf("Detected URL location: %s", location)
		// Read data first for version detection
		resp, err := http.Get(location)
		if err != nil {
			return nil, "", fmt.Errorf("failed to fetch URL '%s': %w", location, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			bodyBytes, _ := io.ReadAll(resp.Body) // Attempt to read body for error context
			return nil, "", fmt.Errorf("failed to fetch URL '%s': status code %d, body: %s", location, resp.StatusCode, string(bodyBytes))
		}
		data, err = io.ReadAll(resp.Body)
		if err != nil {
			return nil, "", fmt.Errorf("failed to read response body from URL '%s': %w", location, err)
		}
	}

	// Detect version from data
	var detector map[string]interface{}
	if err := json.Unmarshal(data, &detector); err != nil {
		return nil, "", fmt.Errorf("failed to parse JSON from '%s' for version detection: %w", location, err)
	}

	if _, ok := detector["openapi"]; ok {
		// OpenAPI 3.x
		loader := openapi3.NewLoader()
		loader.IsExternalRefsAllowed = true
		var doc *openapi3.T
		var loadErr error

		if !isURL {
			// Use LoadFromFile for local files
			utils.SafeLogPrintf("Loading V3 spec using LoadFromFile: %s", absPath)
			doc, loadErr = loader.LoadFromFile(absPath)
		} else {
			// Use LoadFromURI for URLs
			utils.SafeLogPrintf("Loading V3 spec using LoadFromURI: %s", location)
			doc, loadErr = loader.LoadFromURI(locationURL)
		}

		if loadErr != nil {
			return nil, "", fmt.Errorf("failed to load OpenAPI v3 spec from '%s': %w", location, loadErr)
		}

		if err := doc.Validate(context.Background()); err != nil {
			return nil, "", fmt.Errorf("OpenAPI v3 spec validation failed for '%s': %w", location, err)
		}
		return doc, VersionV3, nil
	} else if _, ok := detector["swagger"]; ok {
		// Swagger 2.0 - Still load from data as loads.Analyzed expects bytes
		utils.SafeLogPrintf("Loading V2 spec using loads.Analyzed from data (source: %s)", location)
		doc, err := loads.Analyzed(data, "2.0")
		if err != nil {
			return nil, "", fmt.Errorf("failed to load or validate Swagger v2 spec from '%s': %w", location, err)
		}
		return doc.Spec(), VersionV2, nil
	} else {
		return nil, "", fmt.Errorf("failed to detect OpenAPI/Swagger version in '%s': missing 'openapi' or 'swagger' key", location)
	}
}

// GenerateToolSet converts a loaded spec (v2 or v3) into an MCP ToolSet.
func GenerateToolSet(specDoc interface{}, version string, cfg *config.Config) (*mcp.ToolSet, error) {
	switch version {
	case VersionV3:
		docV3, ok := specDoc.(*openapi3.T)
		if !ok {
			return nil, fmt.Errorf("internal error: expected *openapi3.T for v3 spec, got %T", specDoc)
		}
		return generateToolSetV3(docV3, cfg)
	case VersionV2:
		docV2, ok := specDoc.(*spec.Swagger)
		if !ok {
			return nil, fmt.Errorf("internal error: expected *spec.Swagger for v2 spec, got %T", specDoc)
		}
		return generateToolSetV2(docV2, cfg)
	default:
		return nil, fmt.Errorf("unsupported specification version: %s", version)
	}
}

// --- V3 Specific Implementation ---

func generateToolSetV3(doc *openapi3.T, cfg *config.Config) (*mcp.ToolSet, error) {
	toolSet := createBaseToolSet(doc.Info.Title, doc.Info.Description, cfg)
	toolSet.Operations = make(map[string]mcp.OperationDetail) // Initialize the map

	// Determine Base URL once
	baseURL, err := determineBaseURLV3(doc, cfg)
	if err != nil {
		utils.SafeLogPrintf("Warning: Could not determine base URL for V3 spec: %v. Operations might fail if base URL override is not set.", err)
		baseURL = "" // Allow proceeding if override is set
	}

	// // V3 Handles security differently (Components.SecuritySchemes). Rely on config flags for server-side injection.
	// apiKeyName := cfg.APIKeyName
	// apiKeyIn := string(cfg.APIKeyLocation)
	// // Store detected/configured key details internally - Let config handle this
	// toolSet.SetAPIKeyDetails(apiKeyName, apiKeyIn)

	paths := getSortedPathsV3(doc.Paths)
	for _, rawPath := range paths { // Rename loop var to rawPath
		pathItem := doc.Paths.Value(rawPath)
		for method, op := range pathItem.Operations() {
			if op == nil || !shouldIncludeOperationV3(op, cfg) {
				continue
			}

			// Clean the path
			cleanPath := rawPath
			if queryIndex := strings.Index(rawPath, "?"); queryIndex != -1 {
				cleanPath = rawPath[:queryIndex]
			}

			toolName := generateToolNameV3(op, method, rawPath) // Still generate name from raw path
			toolDesc := getOperationDescriptionV3(op)

			// Convert parameters (query, header, path, cookie)
			parametersSchema, opParams, err := parametersToMCPSchemaAndDetailsV3(op.Parameters, cfg)
			if err != nil {
				return nil, fmt.Errorf("error processing v3 parameters for %s %s: %w", method, rawPath, err)
			}

			// Handle request body
			requestBody, err := requestBodyToMCPV3(op.RequestBody)
			if err != nil {
				utils.SafeLogPrintf("Warning: skipping request body for %s %s due to error: %v", method, rawPath, err)
			} else {
				// Merge request body schema into the main parameter schema
				if requestBody.Content != nil {
					if parametersSchema.Properties == nil {
						parametersSchema.Properties = make(map[string]mcp.Schema)
					}
					for _, mediaTypeSchema := range requestBody.Content {
						if mediaTypeSchema.Type == "object" && mediaTypeSchema.Properties != nil {
							for propName, propSchema := range mediaTypeSchema.Properties {
								parametersSchema.Properties[propName] = propSchema
							}
						} else {
							// If body is not an object, represent as 'requestBody'
							utils.SafeLogPrintf("Warning: V3 request body for %s %s is not an object schema. Representing as 'requestBody' field.", method, rawPath)
							parametersSchema.Properties["requestBody"] = mediaTypeSchema
						}
						break // Only process the first content type
					}

					// Merge required fields from the body *schema* (not the requestBody boolean)
					var bodySchemaRequired []string
					for _, mediaTypeSchema := range requestBody.Content {
						if len(mediaTypeSchema.Required) > 0 {
							bodySchemaRequired = mediaTypeSchema.Required
							break // Use required from the first content type with a schema
						}
					}

					if len(bodySchemaRequired) > 0 {
						if parametersSchema.Required == nil {
							parametersSchema.Required = make([]string, 0)
						}
						for _, r := range bodySchemaRequired { // Range over the correct schema required list
							if !sliceContains(parametersSchema.Required, r) {
								parametersSchema.Required = append(parametersSchema.Required, r)
							}
						}
						sort.Strings(parametersSchema.Required)
					}

					// Optionally, add a note if the requestBody itself was marked as required
					if requestBody.Required { // Check the boolean field
						// How to indicate this? Maybe add to description?
						utils.SafeLogPrintf("Note: Request body for %s %s is marked as required.", method, rawPath)
						// Or add all top-level body props to required? Needs decision.
					}
				}
			}

			// Prepend note about API key handling
			finalToolDesc := "Note: The API key is handled by the server, no need to provide it. " + toolDesc

			tool := mcp.Tool{
				Name:        toolName,
				Description: finalToolDesc,    // Use potentially modified description
				InputSchema: parametersSchema, // Use InputSchema, assuming it contains combined params/body
			}
			toolSet.Tools = append(toolSet.Tools, tool)

			// Store operation details for execution
			toolSet.Operations[toolName] = mcp.OperationDetail{
				Method:     method,
				Path:       cleanPath, // Use the cleaned path here
				BaseURL:    baseURL,
				Parameters: opParams,
			}
		}
	}
	return toolSet, nil
}

func determineBaseURLV3(doc *openapi3.T, cfg *config.Config) (string, error) {
	if cfg.ServerBaseURL != "" {
		return strings.TrimSuffix(cfg.ServerBaseURL, "/"), nil
	}
	if len(doc.Servers) > 0 {
		baseURL := ""
		for _, server := range doc.Servers {
			if baseURL == "" {
				baseURL = server.URL
			}
			if strings.HasPrefix(strings.ToLower(server.URL), "https://") {
				baseURL = server.URL
				break
			}
			if strings.HasPrefix(strings.ToLower(server.URL), "http://") {
				baseURL = server.URL
			}
		}
		if baseURL == "" {
			return "", fmt.Errorf("v3: could not determine a suitable base URL from servers list")
		}
		return strings.TrimSuffix(baseURL, "/"), nil
	}
	return "", fmt.Errorf("v3: no server base URL specified in config or OpenAPI spec servers list")
}

func getSortedPathsV3(paths *openapi3.Paths) []string {
	if paths == nil {
		return []string{}
	}
	keys := make([]string, 0, len(paths.Map()))
	for k := range paths.Map() {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func generateToolNameV3(op *openapi3.Operation, method, path string) string {
	if op.OperationID != "" {
		return op.OperationID
	}
	return generateDefaultToolName(method, path)
}

func getOperationDescriptionV3(op *openapi3.Operation) string {
	if op.Summary != "" {
		return op.Summary
	}
	return op.Description
}

func shouldIncludeOperationV3(op *openapi3.Operation, cfg *config.Config) bool {
	return shouldInclude(op.OperationID, op.Tags, cfg)
}

// parametersToMCPSchemaAndDetailsV3 converts parameters and also returns the parameter details.
func parametersToMCPSchemaAndDetailsV3(params openapi3.Parameters, cfg *config.Config) (mcp.Schema, []mcp.ParameterDetail, error) {
	mcpSchema := mcp.Schema{Type: "object", Properties: make(map[string]mcp.Schema), Required: []string{}}
	opParams := []mcp.ParameterDetail{}
	for _, paramRef := range params {
		if paramRef.Value == nil {
			utils.SafeLogPrintf("Warning: Skipping parameter with nil value.")
			continue
		}
		param := paramRef.Value
		if param.Schema == nil {
			utils.SafeLogPrintf("Warning: Skipping parameter '%s' with nil schema.", param.Name)
			continue
		}

		// Skip the API key parameter if configured
		if cfg.APIKeyName != "" && param.Name == cfg.APIKeyName && param.In == string(cfg.APIKeyLocation) {
			utils.SafeLogPrintf("Parser V3: Skipping API key parameter '%s' ('%s') from input schema generation.", param.Name, param.In)
			continue
		}

		// Store parameter detail (even if skipped for schema, needed for execution?)
		// Decision: Keep storing *all* params in opParams for potential server-side use,
		//           but skip adding the API key to the mcpSchema exposed to the client.
		opParams = append(opParams, mcp.ParameterDetail{
			Name: param.Name,
			In:   param.In,
		})

		propSchema, err := openapiSchemaToMCPSchemaV3(param.Schema)
		if err != nil {
			return mcp.Schema{}, nil, fmt.Errorf("v3 param '%s': %w", param.Name, err)
		}
		propSchema.Description = param.Description
		mcpSchema.Properties[param.Name] = propSchema
		if param.Required {
			mcpSchema.Required = append(mcpSchema.Required, param.Name)
		}
	}
	if len(mcpSchema.Required) > 1 {
		sort.Strings(mcpSchema.Required)
	}
	return mcpSchema, opParams, nil
}

func requestBodyToMCPV3(rbRef *openapi3.RequestBodyRef) (mcp.RequestBody, error) {
	mcpRB := mcp.RequestBody{Content: make(map[string]mcp.Schema)}
	if rbRef == nil || rbRef.Value == nil {
		return mcpRB, nil
	}
	rb := rbRef.Value
	mcpRB.Description = rb.Description
	mcpRB.Required = rb.Required

	var mediaType *openapi3.MediaType
	var chosenMediaTypeKey string
	if mt, ok := rb.Content["application/json"]; ok {
		mediaType, chosenMediaTypeKey = mt, "application/json"
	} else {
		for key, mt := range rb.Content {
			mediaType, chosenMediaTypeKey = mt, key
			break
		}
	}

	if mediaType != nil && mediaType.Schema != nil {
		contentSchema, err := openapiSchemaToMCPSchemaV3(mediaType.Schema)
		if err != nil {
			return mcp.RequestBody{}, fmt.Errorf("v3 request body (media type: %s): %w", chosenMediaTypeKey, err)
		}
		mcpRB.Content["application/json"] = contentSchema
	} else if mediaType != nil {
		mcpRB.Content["application/json"] = mcp.Schema{Type: "string", Description: fmt.Sprintf("Request body with media type %s (no specific schema defined)", chosenMediaTypeKey)}
	}
	return mcpRB, nil
}

func openapiSchemaToMCPSchemaV3(oapiSchemaRef *openapi3.SchemaRef) (mcp.Schema, error) {
	if oapiSchemaRef == nil {
		return mcp.Schema{Type: "string", Description: "Schema reference was nil"}, nil
	}
	if oapiSchemaRef.Value == nil {
		return mcp.Schema{Type: "string", Description: fmt.Sprintf("Schema reference value was nil (ref: %s)", oapiSchemaRef.Ref)}, nil
	}
	oapiSchema := oapiSchemaRef.Value

	var primaryType string
	if oapiSchema.Type != nil && len(*oapiSchema.Type) > 0 {
		primaryType = (*oapiSchema.Type)[0]
	}

	mcpSchema := mcp.Schema{
		Type:        mapJSONSchemaType(primaryType),
		Description: oapiSchema.Description,
		Format:      oapiSchema.Format,
		Enum:        oapiSchema.Enum,
	}

	switch mcpSchema.Type {
	case "object":
		mcpSchema.Properties = make(map[string]mcp.Schema)
		mcpSchema.Required = oapiSchema.Required
		for name, propRef := range oapiSchema.Properties {
			propSchema, err := openapiSchemaToMCPSchemaV3(propRef)
			if err != nil {
				return mcp.Schema{}, fmt.Errorf("v3 object property '%s': %w", name, err)
			}
			mcpSchema.Properties[name] = propSchema
		}
		if len(mcpSchema.Required) > 1 {
			sort.Strings(mcpSchema.Required)
		}
	case "array":
		if oapiSchema.Items != nil {
			itemsSchema, err := openapiSchemaToMCPSchemaV3(oapiSchema.Items)
			if err != nil {
				return mcp.Schema{}, fmt.Errorf("v3 array items: %w", err)
			}
			mcpSchema.Items = &itemsSchema
		}
	case "string", "number", "integer", "boolean", "null":
		// Basic types mapped
	default:
		if mcpSchema.Type == "string" && primaryType != "" && primaryType != "string" {
			mcpSchema.Description += fmt.Sprintf(" (Original type '%s' unknown or unsupported)", primaryType)
		}
	}
	return mcpSchema, nil
}

// --- V2 Specific Implementation ---

func generateToolSetV2(doc *spec.Swagger, cfg *config.Config) (*mcp.ToolSet, error) {
	toolSet := createBaseToolSet(doc.Info.Title, doc.Info.Description, cfg)
	toolSet.Operations = make(map[string]mcp.OperationDetail) // Initialize map

	// Determine Base URL once
	baseURL, err := determineBaseURLV2(doc, cfg)
	if err != nil {
		utils.SafeLogPrintf("Warning: Could not determine base URL for V2 spec: %v. Operations might fail if base URL override is not set.", err)
		baseURL = "" // Allow proceeding if override is set
	}

	// Detect API Key (Security Definitions)
	apiKeyName := cfg.APIKeyName
	apiKeyIn := string(cfg.APIKeyLocation)

	if apiKeyName == "" && apiKeyIn == "" { // Only infer if not provided by config
		for name, secDef := range doc.SecurityDefinitions {
			if secDef.Type == "apiKey" {
				apiKeyName = secDef.Name
				apiKeyIn = secDef.In // "query" or "header"
				utils.SafeLogPrintf("Parser V2: Detected API key from security definition '%s': Name='%s', In='%s'", name, apiKeyName, apiKeyIn)
				break // Assume only one apiKey definition for simplicity
			}
		}
	}
	// Store detected/configured key details internally
	toolSet.SetAPIKeyDetails(apiKeyName, apiKeyIn)

	// --- Iterate through Paths ---
	paths := getSortedPathsV2(doc.Paths)
	for _, rawPath := range paths { // Rename loop var to rawPath
		pathItem := doc.Paths.Paths[rawPath]
		ops := map[string]*spec.Operation{
			"GET":     pathItem.Get,
			"PUT":     pathItem.Put,
			"POST":    pathItem.Post,
			"DELETE":  pathItem.Delete,
			"OPTIONS": pathItem.Options,
			"HEAD":    pathItem.Head,
			"PATCH":   pathItem.Patch,
		}

		for method, op := range ops {
			if op == nil || !shouldIncludeOperationV2(op, cfg) {
				continue
			}

			// Clean the path
			cleanPath := rawPath
			if queryIndex := strings.Index(rawPath, "?"); queryIndex != -1 {
				cleanPath = rawPath[:queryIndex]
			}

			toolName := generateToolNameV2(op, method, rawPath) // Still generate name from raw path
			toolDesc := getOperationDescriptionV2(op)

			// Convert parameters and potential body schema
			parametersSchema, bodySchema, opParams, err := parametersToMCPSchemaAndDetailsV2(op.Parameters, doc.Definitions, apiKeyName)
			if err != nil {
				return nil, fmt.Errorf("error processing v2 parameters for %s %s: %w", method, rawPath, err)
			}

			// Combine request body into parameters schema if it exists
			if bodySchema.Type != "" { // Check if bodySchema was actually populated
				if bodySchema.Type == "object" && bodySchema.Properties != nil {
					if parametersSchema.Properties == nil {
						parametersSchema.Properties = make(map[string]mcp.Schema)
					}
					for propName, propSchema := range bodySchema.Properties {
						parametersSchema.Properties[propName] = propSchema
					}
					if len(bodySchema.Required) > 0 {
						if parametersSchema.Required == nil {
							parametersSchema.Required = make([]string, 0)
						}
						for _, r := range bodySchema.Required {
							if !sliceContains(parametersSchema.Required, r) {
								parametersSchema.Required = append(parametersSchema.Required, r)
							}
						}
						sort.Strings(parametersSchema.Required)
					}
				} else {
					// If body is not an object, represent as 'requestBody'
					utils.SafeLogPrintf("Warning: V2 request body for %s %s is not an object schema. Representing as 'requestBody' field.", method, rawPath)
					if parametersSchema.Properties == nil {
						parametersSchema.Properties = make(map[string]mcp.Schema)
					}
					parametersSchema.Properties["requestBody"] = bodySchema
				}
			}

			// Prepend note about API key handling
			finalToolDesc := "Note: The API key is handled by the server, no need to provide it. " + toolDesc

			tool := mcp.Tool{
				Name:        toolName,
				Description: finalToolDesc,    // Use potentially modified description
				InputSchema: parametersSchema, // Use InputSchema, assuming it contains combined params/body
			}
			toolSet.Tools = append(toolSet.Tools, tool)

			// Store operation details for execution
			toolSet.Operations[toolName] = mcp.OperationDetail{
				Method:     method,
				Path:       cleanPath, // Use the cleaned path here
				BaseURL:    baseURL,
				Parameters: opParams,
			}
		}
	}

	return toolSet, nil
}

func determineBaseURLV2(doc *spec.Swagger, cfg *config.Config) (string, error) {
	if cfg.ServerBaseURL != "" {
		return strings.TrimSuffix(cfg.ServerBaseURL, "/"), nil
	}

	host := doc.Host
	if host == "" {
		return "", fmt.Errorf("v2: missing 'host' in spec")
	}

	scheme := "https"
	if len(doc.Schemes) > 0 {
		// Prefer https, then http, then first
		preferred := []string{"https", "http"}
		found := false
		for _, p := range preferred {
			for _, s := range doc.Schemes {
				if s == p {
					scheme = s
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
			scheme = doc.Schemes[0]
		} // fallback to first scheme
	} // else default to https

	basePath := doc.BasePath

	return strings.TrimSuffix(scheme+"://"+host+basePath, "/"), nil
}

func getSortedPathsV2(paths *spec.Paths) []string {
	if paths == nil {
		return []string{}
	}
	keys := make([]string, 0, len(paths.Paths))
	for k := range paths.Paths {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func generateToolNameV2(op *spec.Operation, method, path string) string {
	if op.ID != "" {
		return op.ID
	}
	return generateDefaultToolName(method, path)
}

func getOperationDescriptionV2(op *spec.Operation) string {
	if op.Summary != "" {
		return op.Summary
	}
	return op.Description
}

func shouldIncludeOperationV2(op *spec.Operation, cfg *config.Config) bool {
	return shouldInclude(op.ID, op.Tags, cfg)
}

// parametersToMCPSchemaAndDetailsV2 converts V2 parameters and also returns details and request body.
func parametersToMCPSchemaAndDetailsV2(params []spec.Parameter, definitions spec.Definitions, apiKeyName string) (mcp.Schema, mcp.Schema, []mcp.ParameterDetail, error) {
	mcpSchema := mcp.Schema{Type: "object", Properties: make(map[string]mcp.Schema), Required: []string{}}
	bodySchema := mcp.Schema{} // Initialize empty
	opParams := []mcp.ParameterDetail{}
	hasBodyParam := false
	var bodyParam *spec.Parameter // Declare bodyParam here to be accessible later

	// First pass: Separate body param, process others
	for _, param := range params {
		// Skip the API key parameter if it's configured/detected
		if apiKeyName != "" && param.Name == apiKeyName && (param.In == "query" || param.In == "header") {
			utils.SafeLogPrintf("Parser V2: Skipping API key parameter '%s' ('%s') from input schema generation.", param.Name, param.In)
			continue
		}

		if param.In == "body" {
			if hasBodyParam {
				return mcp.Schema{}, mcp.Schema{}, nil, fmt.Errorf("v2: multiple 'body' parameters found")
			}
			hasBodyParam = true
			bodyParam = &param // Assign to outer scope variable
			continue           // Don't process body param further in this loop
		}

		if param.In != "query" && param.In != "path" && param.In != "header" && param.In != "formData" {
			utils.SafeLogPrintf("Parser V2: Skipping unsupported parameter type '%s' for parameter '%s'", param.In, param.Name)
			continue
		}

		// Add non-body param detail
		opParams = append(opParams, mcp.ParameterDetail{
			Name: param.Name,
			In:   param.In, // query, header, path, formData
		})

		// Convert non-body param schema and add to mcpSchema
		propSchema, err := swaggerParamToMCPSchema(&param, definitions)
		if err != nil {
			return mcp.Schema{}, mcp.Schema{}, nil, fmt.Errorf("v2 param '%s': %w", param.Name, err)
		}
		mcpSchema.Properties[param.Name] = propSchema
		if param.Required {
			mcpSchema.Required = append(mcpSchema.Required, param.Name)
		}
	}

	// Second pass: Process the body parameter if found
	if bodyParam != nil {
		bodySchema.Description = bodyParam.Description

		if bodyParam.Schema != nil {
			// Convert the body schema (resolving $refs)
			bodySchemaFields, err := swaggerSchemaToMCPSchemaV2(bodyParam.Schema, definitions)
			if err != nil {
				return mcp.Schema{}, mcp.Schema{}, nil, fmt.Errorf("v2 request body schema: %w", err)
			}
			// Update our local bodySchema with the converted fields
			bodySchema.Type = bodySchemaFields.Type
			bodySchema.Properties = bodySchemaFields.Properties
			bodySchema.Items = bodySchemaFields.Items
			bodySchema.Format = bodySchemaFields.Format
			bodySchema.Enum = bodySchemaFields.Enum
			bodySchema.Required = bodySchemaFields.Required // Required fields from the *schema* itself

			// Merge bodySchema properties into the main mcpSchema
			if bodySchema.Type == "object" && bodySchema.Properties != nil {
				for propName, propSchema := range bodySchema.Properties {
					mcpSchema.Properties[propName] = propSchema
				}
				// Merge required fields from the body's schema into the main required list
				if len(bodySchema.Required) > 0 {
					mcpSchema.Required = append(mcpSchema.Required, bodySchema.Required...)
				}
			} else {
				// Handle non-object body schema (e.g., array, string)
				// Add a single property named after the body parameter
				mcpSchema.Properties[bodyParam.Name] = bodySchemaFields // Use the converted schema
				if bodyParam.Required {                                 // Check the parameter's required status
					mcpSchema.Required = append(mcpSchema.Required, bodyParam.Name)
				}
			}

		} else {
			// Body param defined without a schema? Treat as simple string.
			utils.SafeLogPrintf("Warning: V2 body parameter '%s' defined without a schema. Treating as string.", bodyParam.Name)
			bodySchema.Type = "string"
			mcpSchema.Properties[bodyParam.Name] = bodySchema
			if bodyParam.Required {
				mcpSchema.Required = append(mcpSchema.Required, bodyParam.Name)
			}
		}

		// Always add the body parameter to the OperationDetail list
		opParams = append(opParams, mcp.ParameterDetail{
			Name: bodyParam.Name,
			In:   bodyParam.In,
		})
	}

	// Sort and deduplicate the final required list
	if len(mcpSchema.Required) > 1 {
		sort.Strings(mcpSchema.Required)
		seen := make(map[string]struct{}, len(mcpSchema.Required))
		j := 0
		for _, r := range mcpSchema.Required {
			if _, ok := seen[r]; !ok {
				seen[r] = struct{}{}
				mcpSchema.Required[j] = r
				j++
			}
		}
		mcpSchema.Required = mcpSchema.Required[:j]
	}

	return mcpSchema, bodySchema, opParams, nil
}

// swaggerParamToMCPSchema converts a V2 Parameter (non-body) to an MCP Schema.
func swaggerParamToMCPSchema(param *spec.Parameter, definitions spec.Definitions) (mcp.Schema, error) {
	// This needs to handle types like string, integer, array based on param.Type, param.Format, param.Items
	// Simplified version:
	mcpSchema := mcp.Schema{
		Type:        mapJSONSchemaType(param.Type), // Use the same mapping
		Description: param.Description,
		Format:      param.Format,
		Enum:        param.Enum,
		// TODO: Map items for array type, map constraints (maximum, etc.)
	}
	if param.Type == "array" && param.Items != nil {
		// Need to convert param.Items (which is *spec.Items) to MCP schema
		itemsSchema, err := swaggerItemsToMCPSchema(param.Items, definitions)
		if err != nil {
			return mcp.Schema{}, fmt.Errorf("v2 array param '%s' items: %w", param.Name, err)
		}
		mcpSchema.Items = &itemsSchema
	}
	return mcpSchema, nil
}

// swaggerItemsToMCPSchema converts V2 Items object
func swaggerItemsToMCPSchema(items *spec.Items, definitions spec.Definitions) (mcp.Schema, error) {
	if items == nil {
		return mcp.Schema{Type: "string", Description: "nil items"}, nil
	}
	// Similar logic to swaggerParamToMCPSchema but for Items structure
	mcpSchema := mcp.Schema{
		Type:        mapJSONSchemaType(items.Type),
		Description: "", // Items don't have descriptions typically
		Format:      items.Format,
		Enum:        items.Enum,
	}
	if items.Type == "array" && items.Items != nil {
		subItemsSchema, err := swaggerItemsToMCPSchema(items.Items, definitions)
		if err != nil {
			return mcp.Schema{}, fmt.Errorf("v2 nested array items: %w", err)
		}
		mcpSchema.Items = &subItemsSchema
	}
	// TODO: Handle $ref within items? Not directly supported by spec.Items
	return mcpSchema, nil
}

// swaggerSchemaToMCPSchemaV2 converts a Swagger v2 schema (from definitions or body param) to mcp.Schema
func swaggerSchemaToMCPSchemaV2(oapiSchema *spec.Schema, definitions spec.Definitions) (mcp.Schema, error) {
	if oapiSchema == nil {
		return mcp.Schema{Type: "string", Description: "Schema was nil"}, nil
	}

	// Handle $ref
	if oapiSchema.Ref.String() != "" {
		refSchema, err := resolveRefV2(oapiSchema.Ref, definitions)
		if err != nil {
			return mcp.Schema{}, err
		}
		// Recursively convert the resolved schema, careful with cycles
		return swaggerSchemaToMCPSchemaV2(refSchema, definitions)
	}

	var primaryType string
	if len(oapiSchema.Type) > 0 {
		primaryType = oapiSchema.Type[0]
	}

	mcpSchema := mcp.Schema{
		Type:        mapJSONSchemaType(primaryType),
		Description: oapiSchema.Description,
		Format:      oapiSchema.Format,
		Enum:        oapiSchema.Enum,
		// TODO: Map V2 constraints (Maximum, Minimum, etc.)
	}

	switch mcpSchema.Type {
	case "object":
		mcpSchema.Properties = make(map[string]mcp.Schema)
		mcpSchema.Required = oapiSchema.Required
		for name, propSchema := range oapiSchema.Properties {
			// propSchema here is spec.Schema, need recursive call
			propMCPSchema, err := swaggerSchemaToMCPSchemaV2(&propSchema, definitions)
			if err != nil {
				return mcp.Schema{}, fmt.Errorf("v2 object property '%s': %w", name, err)
			}
			mcpSchema.Properties[name] = propMCPSchema
		}
		if len(mcpSchema.Required) > 1 {
			sort.Strings(mcpSchema.Required)
		}
	case "array":
		if oapiSchema.Items != nil && oapiSchema.Items.Schema != nil {
			// V2 Items has a single Schema field
			itemsSchema, err := swaggerSchemaToMCPSchemaV2(oapiSchema.Items.Schema, definitions)
			if err != nil {
				return mcp.Schema{}, fmt.Errorf("v2 array items: %w", err)
			}
			mcpSchema.Items = &itemsSchema
		} else if oapiSchema.Items != nil && len(oapiSchema.Items.Schemas) > 0 {
			// Handle tuple-like arrays (less common, maybe simplify to single type?)
			// For now, take the first schema
			itemsSchema, err := swaggerSchemaToMCPSchemaV2(&oapiSchema.Items.Schemas[0], definitions)
			if err != nil {
				return mcp.Schema{}, fmt.Errorf("v2 tuple array items: %w", err)
			}
			mcpSchema.Items = &itemsSchema
			mcpSchema.Description += " (Note: original was tuple-like array, showing first type)"
		}
	case "string", "number", "integer", "boolean", "null":
		// Basic types mapped
	default:
		if mcpSchema.Type == "string" && primaryType != "" && primaryType != "string" {
			mcpSchema.Description += fmt.Sprintf(" (Original type '%s' unknown or unsupported)", primaryType)
		}
	}
	return mcpSchema, nil
}

func resolveRefV2(ref spec.Ref, definitions spec.Definitions) (*spec.Schema, error) {
	// Simple local definition resolution
	refStr := ref.String()
	if !strings.HasPrefix(refStr, "#/definitions/") {
		return nil, fmt.Errorf("unsupported $ref format: %s", refStr)
	}
	defName := strings.TrimPrefix(refStr, "#/definitions/")
	schema, ok := definitions[defName]
	if !ok {
		return nil, fmt.Errorf("$ref '%s' not found in definitions", refStr)
	}
	return &schema, nil
}

// --- Common Helper Functions ---

func createBaseToolSet(title, desc string, cfg *config.Config) *mcp.ToolSet {
	// Prioritize config overrides if they are set
	toolSetName := title // Default to spec title
	if cfg.DefaultToolName != "" {
		toolSetName = cfg.DefaultToolName // Use config override if provided
	}

	toolSetDesc := desc // Default to spec description
	if cfg.DefaultToolDesc != "" {
		toolSetDesc = cfg.DefaultToolDesc // Use config override if provided
	}

	toolSet := &mcp.ToolSet{
		MCPVersion:  "0.1.0",
		Name:        toolSetName, // Use determined name
		Description: toolSetDesc, // Use determined description
		Tools:       []mcp.Tool{},
		Operations:  make(map[string]mcp.OperationDetail), // Initialize map
	}

	// The old overwrite logic is removed as it's handled above
	// if title != "" {
	// 	toolSet.Name = title
	// }
	// if desc != "" {
	// 	toolSet.Description = desc
	// }
	return toolSet
}

// generateDefaultToolName creates a name if operationId is missing.
func generateDefaultToolName(method, path string) string {
	pathParts := strings.Split(strings.Trim(path, "/"), "/")
	var nameParts []string
	nameParts = append(nameParts, strings.ToUpper(method[:1])+strings.ToLower(method[1:]))
	for _, part := range pathParts {
		if part == "" {
			continue
		}
		if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
			paramName := strings.Trim(part, "{}")
			nameParts = append(nameParts, "By"+strings.ToUpper(paramName[:1])+paramName[1:])
		} else {
			sanitizedPart := strings.ReplaceAll(part, "-", "_")
			sanitizedPart = strings.Title(sanitizedPart) // Basic capitalization
			nameParts = append(nameParts, sanitizedPart)
		}
	}
	return strings.Join(nameParts, "")
}

// shouldInclude determines if an operation should be included based on config filters.
func shouldInclude(opID string, opTags []string, cfg *config.Config) bool {
	// Exclusion rules take precedence
	if len(cfg.ExcludeOperations) > 0 && opID != "" && sliceContains(cfg.ExcludeOperations, opID) {
		return false
	}
	if len(cfg.ExcludeTags) > 0 {
		for _, tag := range opTags {
			if sliceContains(cfg.ExcludeTags, tag) {
				return false
			}
		}
	}

	// Inclusion rules
	hasInclusionRule := len(cfg.IncludeOperations) > 0 || len(cfg.IncludeTags) > 0
	if !hasInclusionRule {
		return true
	} // No inclusion rules, include by default

	if len(cfg.IncludeOperations) > 0 {
		if opID != "" && sliceContains(cfg.IncludeOperations, opID) {
			return true
		}
	} else if len(cfg.IncludeTags) > 0 {
		for _, tag := range opTags {
			if sliceContains(cfg.IncludeTags, tag) {
				return true
			}
		}
	}
	return false // Did not match any inclusion rule
}

// mapJSONSchemaType ensures the type is one recognized by JSON Schema / MCP.
func mapJSONSchemaType(oapiType string) string {
	switch strings.ToLower(oapiType) { // Normalize type
	case "integer", "number", "string", "boolean", "array", "object":
		return strings.ToLower(oapiType)
	case "null":
		return "string" // Represent null as string for MCP?
	case "file": // Swagger 2.0 specific type
		return "string" // Represent file uploads as string (e.g., path or content)?
	default:
		return "string"
	}
}

// sliceContains checks if a string slice contains a specific string.
func sliceContains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
