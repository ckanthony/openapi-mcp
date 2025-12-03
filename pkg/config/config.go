package config

import (
	"os"

	"github.com/ckanthony/openapi-mcp/pkg/utils"
)

// APIKeyLocation specifies where the API key is located for requests.
type APIKeyLocation string

const (
	APIKeyLocationHeader APIKeyLocation = "header"
	APIKeyLocationQuery  APIKeyLocation = "query"
	APIKeyLocationPath   APIKeyLocation = "path"
	APIKeyLocationCookie APIKeyLocation = "cookie"
	// APIKeyLocationCookie APIKeyLocation = "cookie" // Add if needed
)

// Config holds the configuration for generating the MCP toolset.
type Config struct {
	SpecPath string // Path or URL to the OpenAPI specification file.

	// API Key details (optional, inferred from spec if possible)
	APIKey           string         // The actual API key value.
	APIKeyName       string         // Name of the header or query parameter for the API key (e.g., "X-API-Key", "api_key").
	APIKeyLocation   APIKeyLocation // Where the API key should be placed (header, query, path, or cookie).
	APIKeyFromEnvVar string         // Environment variable name to read the API key from.

	// Filtering (optional)
	IncludeTags       []string // Only include operations with these tags.
	ExcludeTags       []string // Exclude operations with these tags.
	IncludeOperations []string // Only include operations with these IDs.
	ExcludeOperations []string // Exclude operations with these IDs.

	// Overrides (optional)
	ServerBaseURL   string // Manually override the base URL for API calls, ignoring the spec's servers field.
	DefaultToolName string // Name for the toolset if not specified in the spec's info section.
	DefaultToolDesc string // Description for the toolset if not specified in the spec's info section.

	// Server-side request modification
	CustomHeaders string // Comma-separated list of headers (e.g., "Header1:Value1,Header2:Value2") to add to outgoing requests.
}

// GetAPIKey resolves the API key value, prioritizing the environment variable over the direct flag.
func (c *Config) GetAPIKey() string {
	utils.SafeLogPrintf("GetAPIKey: Attempting to resolve API key...")

	// 1. Check environment variable specified by --api-key-env
	if c.APIKeyFromEnvVar != "" {
		utils.SafeLogPrintf("GetAPIKey: Checking environment variable specified by --api-key-env: %s", c.APIKeyFromEnvVar)
		val := os.Getenv(c.APIKeyFromEnvVar)
		if val != "" {
			utils.SafeLogPrintf("GetAPIKey: Found key in environment variable %s.", c.APIKeyFromEnvVar)
			return val
		}
		utils.SafeLogPrintf("GetAPIKey: Environment variable %s not found or empty.", c.APIKeyFromEnvVar)
	} else {
		utils.SafeLogPrintf("GetAPIKey: No --api-key-env variable specified.")
	}

	// 2. Check direct flag --api-key
	if c.APIKey != "" {
		utils.SafeLogPrintf("GetAPIKey: Found key provided directly via --api-key flag.")
		return c.APIKey
	}

	// 3. No key found
	utils.SafeLogPrintf("GetAPIKey: No API key found from config (env var or direct flag).")
	return ""
}
