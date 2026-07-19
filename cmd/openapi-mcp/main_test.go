package main

import (
	"strings"
	"testing"

	"github.com/ckanthony/openapi-mcp/pkg/config"
)

func TestConfigurationSummaryRedactsSecrets(t *testing.T) {
	cfg := &config.Config{
		SpecPath:          "https://spec-secret@example.com/openapi.json?token=query-secret",
		APIKey:            "direct-secret",
		APIKeyFromEnvVar:  "API_TOKEN",
		APIKeyName:        "x-api-key",
		APIKeyLocation:    config.APIKeyLocationHeader,
		IncludeTags:       []string{"Tweets", "Trends"},
		ExcludeTags:       []string{"Admin"},
		IncludeOperations: []string{"searchTweets"},
		ExcludeOperations: []string{"deleteTweet"},
		CustomHeaders:     "Authorization:Bearer secret-header",
	}

	summary := configurationSummary(cfg)

	for _, secret := range []string{
		"direct-secret",
		"secret-header",
		"API_TOKEN",
		"spec-secret",
		"query-secret",
	} {
		if strings.Contains(summary, secret) {
			t.Fatalf("configuration summary exposed secret value %q", secret)
		}
	}

	for _, field := range []string{
		"spec_configured=true",
		"api_key_source=environment",
		`api_key_name="x-api-key"`,
		`api_key_location="header"`,
		"include_tags=2",
		"exclude_tags=1",
		"include_operations=1",
		"exclude_operations=1",
		"custom_headers_configured=true",
	} {
		if !strings.Contains(summary, field) {
			t.Errorf("configuration summary missing %q: %s", field, summary)
		}
	}
}

func TestConfigurationSummaryReportsDirectFlag(t *testing.T) {
	cfg := &config.Config{APIKey: "direct-secret"}

	summary := configurationSummary(cfg)

	if !strings.Contains(summary, "api_key_source=flag") {
		t.Fatalf("configuration summary should report the direct flag source: %s", summary)
	}
	if strings.Contains(summary, cfg.APIKey) {
		t.Fatalf("configuration summary exposed the direct API key")
	}
}
