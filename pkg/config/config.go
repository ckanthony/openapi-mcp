package config

import (
	"os"

	"github.com/ckanthony/openapi-mcp/pkg/logx"
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

	// Server URL detection
	ServerBaseURL   string // Manually override the base URL for API calls, ignoring the spec's servers field.
	DefaultToolName string // Name for the toolset if not specified in the spec's info section.
	DefaultToolDesc string // Description for the toolset if not specified in the spec's info section.

	// InsecureSkipVerify disables TLS certificate verification for outgoing
	// requests (e.g. self-signed HTTPS targets like a local mofli device).
	InsecureSkipVerify bool

	// Server-side request modification
	CustomHeaders string // Comma-separated list of headers (e.g., "Header1:Value1,Header2:Value2") to add to outgoing requests.

	// Session token (login-derived) injected into outgoing requests. This is a
	// per-call value computed by the registry's login flow, not stored config.
	SessionToken         string         // The token value returned by the API's login operation.
	SessionTokenName     string         // Header/query/cookie parameter name (default "Authorization").
	SessionTokenLocation APIKeyLocation // Where to attach it (default header).
	SessionTokenPrefix   string         // Prefix prepended to the value (default "Bearer " for Authorization).
}

// GetAPIKey resolves the API key value, prioritizing the environment variable over the direct flag.
func (c *Config) GetAPIKey() string {
	log := logx.Module("config")
	log.Debug("resolving API key")

	// 1. Check environment variable specified by --api-key-env
	if c.APIKeyFromEnvVar != "" {
		log.Debug("checking api key environment variable", "env", c.APIKeyFromEnvVar)
		val := os.Getenv(c.APIKeyFromEnvVar)
		if val != "" {
			log.Debug("found API key in environment variable", "env", c.APIKeyFromEnvVar)
			return val
		}
		log.Debug("api key environment variable not found or empty", "env", c.APIKeyFromEnvVar)
	} else {
		log.Debug("no --api-key-env variable specified")
	}

	// 2. Check direct flag --api-key
	if c.APIKey != "" {
		log.Debug("API key provided directly")
		return c.APIKey
	}

	// 3. No key found
	log.Debug("no API key found from config (env var or direct flag)")
	return ""
}
