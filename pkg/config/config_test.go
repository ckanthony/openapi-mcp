package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestConfig_GetAPIKey(t *testing.T) {
	tests := []struct {
		name        string
		config      Config
		envKey      string // Environment variable name to set
		envValue    string // Value to set for the env var
		expectedKey string
		cleanupEnv  bool // Flag to indicate if env var needs cleanup
	}{
		{
			name:        "No key set",
			config:      Config{}, // Empty config
			expectedKey: "",
		},
		{
			name: "Direct key set only",
			config: Config{
				APIKey: "direct-key-123",
			},
			expectedKey: "direct-key-123",
		},
		{
			name: "Env var set only",
			config: Config{
				APIKeyFromEnvVar: "TEST_API_KEY_ENV_ONLY",
			},
			envKey:      "TEST_API_KEY_ENV_ONLY",
			envValue:    "env-key-456",
			expectedKey: "env-key-456",
			cleanupEnv:  true,
		},
		{
			name: "Both direct and env var set (env takes precedence)",
			config: Config{
				APIKey:           "direct-key-789",
				APIKeyFromEnvVar: "TEST_API_KEY_BOTH",
			},
			envKey:      "TEST_API_KEY_BOTH",
			envValue:    "env-key-abc",
			expectedKey: "env-key-abc",
			cleanupEnv:  true,
		},
		{
			name: "Direct key set, env var specified but not set",
			config: Config{
				APIKey:           "direct-key-xyz",
				APIKeyFromEnvVar: "TEST_API_KEY_UNSET",
			},
			envKey:      "TEST_API_KEY_UNSET", // Ensure this is not set
			envValue:    "",
			expectedKey: "direct-key-xyz", // Should fall back to direct key
			cleanupEnv:  true,             // Cleanup in case it was set previously
		},
		{
			name: "Env var specified but empty string value",
			config: Config{
				APIKeyFromEnvVar: "TEST_API_KEY_EMPTY",
			},
			envKey:      "TEST_API_KEY_EMPTY",
			envValue:    "", // Explicitly set to empty string
			expectedKey: "", // Empty env var should result in empty key
			cleanupEnv:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Set environment variable if needed for this test case
			if tc.envKey != "" {
				originalValue, wasSet := os.LookupEnv(tc.envKey)
				err := os.Setenv(tc.envKey, tc.envValue)
				if err != nil {
					t.Fatalf("Failed to set environment variable %s: %v", tc.envKey, err)
				}
				// Schedule cleanup
				if tc.cleanupEnv {
					t.Cleanup(func() {
						if wasSet {
							os.Setenv(tc.envKey, originalValue)
						} else {
							os.Unsetenv(tc.envKey)
						}
					})
				}
			} else {
				// Ensure env var is unset if tc.envKey is empty (for tests like "Direct key set only")
				// This prevents interference from previous tests if not cleaned up properly.
				os.Unsetenv(tc.config.APIKeyFromEnvVar) // Unset based on config field if relevant
			}

			// Call the method under test
			actualKey := tc.config.GetAPIKey()

			// Assert the result
			if actualKey != tc.expectedKey {
				t.Errorf("Expected API key %q, but got %q", tc.expectedKey, actualKey)
			}
		})
	}
}

func TestFileConfigRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	fc := &FileConfig{APIs: []APIDefinition{
		{
			Name:         "weather",
			Source:       "https://spec.example.com/weather.json",
			ActiveTarget: "prod",
			IncludeTags:  []string{"public"},
			Auth: AuthConfig{
				Type: AuthAPIKey,
				In:   "query",
				Name: "key",
			},
			Monitoring: MonitorConfig{
				Enabled:    true,
				AutoReload: true,
			},
			Targets: []TargetDefinition{
				{Name: "prod", BaseURL: "https://prod.example.com", APIKeyEnv: "W_KEY"},
				{Name: "staging", BaseURL: "https://staging.example.com"},
			},
		},
	}}

	require.NoError(t, SaveFile(path, fc))
	// The persisted file must be YAML, not JSON.
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), `"apis":`)

	loaded, err := LoadFile(path)
	require.NoError(t, err)
	require.Len(t, loaded.APIs, 1)
	got := loaded.APIs[0]
	assert.Equal(t, "weather", got.Name)
	assert.Equal(t, "prod", got.ActiveTarget)
	assert.Equal(t, "https://prod.example.com", got.Targets[0].BaseURL)
	assert.Equal(t, "query", got.Auth.Effective().In)
	assert.Equal(t, "query", got.Auth.In)

	cfg := got.Targets[0].ToConfig()
	assert.Equal(t, "https://prod.example.com", cfg.ServerBaseURL)
	assert.Equal(t, "W_KEY", cfg.APIKeyFromEnvVar)

	assert.False(t, got.Targets[0].InsecureSkipVerify)
	assert.False(t, cfg.InsecureSkipVerify)

	assert.True(t, got.Monitoring.Enabled)
	assert.True(t, got.Monitoring.AutoReload)
}

func TestFileConfigLoadsJSON(t *testing.T) {
	// yaml.v3 also parses a plain JSON file, so legacy configs keep working.
	path := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(path, []byte(`{"apis":[{"name":"legacy","spec":"{}"}]}`), 0644)
	fc, err := LoadFile(path)
	require.NoError(t, err)
	require.Len(t, fc.APIs, 1)
	assert.Equal(t, "legacy", fc.APIs[0].Name)
}

func TestToConfigCustomHeaders(t *testing.T) {
	target := TargetDefinition{
		CustomHeaders: map[string]string{"X-Trace": "abc", "X-Tenant": "t1"},
	}
	cfg := target.ToConfig()
	assert.Contains(t, cfg.CustomHeaders, "X-Trace:abc")
	assert.Contains(t, cfg.CustomHeaders, "X-Tenant:t1")
}

func TestLoadFileMissingAndInvalid(t *testing.T) {
	_, err := LoadFile(filepath.Join(t.TempDir(), "missing.json"))
	assert.ErrorContains(t, err, "reading config file")

	path := filepath.Join(t.TempDir(), "bad.json")
	os.WriteFile(path, []byte("{not json"), 0644)
	_, err = LoadFile(path)
	assert.ErrorContains(t, err, "parsing config file")
}

func TestKnowledgeConfigRoundTrip(t *testing.T) {
	kc := KnowledgeConfig{
		Enabled:  true,
		Language: "es",
		Root:     "/srv/kb/acme",
		Backend: KnowledgeBackendConfig{
			Type:          "git",
			RepositoryEnv: "F_KB_REPO",
			Branch:        "dev",
			Sync:          "auto",
			Conflict:      "rebase",
		},
		Learning: KnowledgeLearningConfig{Enabled: true},
	}
	fc := &FileConfig{APIs: []APIDefinition{{Name: "acme", Knowledge: kc}}}
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, SaveFile(path, fc))
	got, err := LoadFile(path)
	require.NoError(t, err)
	require.Len(t, got.APIs, 1)
	assert.Equal(t, kc, got.APIs[0].Knowledge)
}

func TestMetaConfigRoundTrip(t *testing.T) {
	fc := &FileConfig{
		Meta: &MetaConfig{Knowledge: KnowledgeConfig{
			Enabled:  true,
			Language: "es",
			Root:     "/srv/kb/_meta",
			Backend: KnowledgeBackendConfig{
				Type:          "git",
				RepositoryEnv: "META_KB_REPO",
				Branch:        "main",
				Sync:          "auto",
				Conflict:      "rebase",
			},
		}},
		APIs: []APIDefinition{{Name: "acme", Spec: "{}"}},
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, SaveFile(path, fc))

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "META_KB_REPO")

	got, err := LoadFile(path)
	require.NoError(t, err)
	require.NotNil(t, got.Meta)
	assert.Equal(t, fc.Meta, got.Meta)
	assert.Equal(t, "/srv/kb/_meta", got.Meta.Knowledge.Root)
}

func TestNormalizeKnowledgeConfigDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	kc := NormalizeKnowledgeConfig(KnowledgeConfig{Enabled: true}, "_meta", "")
	assert.Equal(t, filepath.Join(home, ".config", "openapi-mcp", "knowledge", "_meta"), kc.Root)

	kc = NormalizeKnowledgeConfig(KnowledgeConfig{Enabled: true}, "acme", "/etc/openapi-mcp")
	assert.Equal(t, filepath.Join("/etc/openapi-mcp", "knowledge", "acme"), kc.Root)

	// An explicit Root wins over the default.
	kc = NormalizeKnowledgeConfig(KnowledgeConfig{Enabled: true, Root: "/custom"}, "_meta", "/etc/openapi-mcp")
	assert.Equal(t, "/custom", kc.Root)
}

func TestResolveKnowledgeBackendEnvPrecedence(t *testing.T) {
	t.Setenv("KB_TEST_REPO", "https://git.example/kb.git")
	kc := KnowledgeConfig{
		Backend: KnowledgeBackendConfig{
			Type:          "git",
			Repository:    "literal://x",
			RepositoryEnv: "KB_TEST_REPO",
		},
	}
	got := kc.ResolveKnowledgeBackend()
	assert.Equal(t, "https://git.example/kb.git", got.Repository) // env wins
	assert.Equal(t, "main", got.Branch)
	assert.Equal(t, "auto", got.Sync)
	assert.Equal(t, "rebase", got.Conflict)
}

func TestUIServerConfigDefaults(t *testing.T) {
	var u UIServerConfig
	assert.True(t, u.IsEnabled(), "UI enabled by default")
	assert.Equal(t, "X-Ui-Session", u.ResolveSessionHeader())
	assert.Equal(t, 100, u.ResolveMaxSessions())
	assert.Equal(t, "", u.ResolveToken())

	disabled := false
	u2 := UIServerConfig{Enabled: &disabled, SessionHeader: "X-Custom", MaxSessions: 2, TokenEnv: "UI_DEFAULTS_TEST_TOKEN"}
	assert.False(t, u2.IsEnabled())
	assert.Equal(t, "X-Custom", u2.ResolveSessionHeader())
	assert.Equal(t, 2, u2.ResolveMaxSessions())

	t.Setenv("UI_DEFAULTS_TEST_TOKEN", "abc")
	assert.Equal(t, "abc", u2.ResolveToken())
}

func TestFeaturesConfigDefaults(t *testing.T) {
	// Every production feature is on by default; only the beta web UI is off.
	var f FeaturesConfig
	assert.False(t, f.WebUIEnabled(), "web UI is beta -> disabled by default")
	assert.True(t, f.KnowledgeEnabled())
	assert.True(t, f.MetaEnabled())
	assert.True(t, f.ScriptsEnabled())
	assert.True(t, f.APIRegistrationEnabled())
	assert.True(t, f.APIIntrospectionEnabled())
	assert.True(t, f.APIExposureEnabled())

	var sc ServerConfig
	assert.False(t, sc.WebUIEnabled(), "ServeMCP must not mount the UI by default")
}

func TestFeaturesConfigOverrides(t *testing.T) {
	off, on := false, true
	f := FeaturesConfig{
		WebUI:            &off,
		Knowledge:        &off,
		Meta:             &off,
		Scripts:          &off,
		APIRegistration:  &off,
		APIIntrospection: &off,
		APIExposure:      &off,
	}
	assert.False(t, f.WebUIEnabled())
	assert.False(t, f.KnowledgeEnabled())
	assert.False(t, f.MetaEnabled())
	assert.False(t, f.ScriptsEnabled())
	assert.False(t, f.APIRegistrationEnabled())
	assert.False(t, f.APIIntrospectionEnabled())
	assert.False(t, f.APIExposureEnabled())

	f2 := FeaturesConfig{WebUI: &on}
	assert.True(t, f2.WebUIEnabled())
	assert.True(t, f2.KnowledgeEnabled(), "unset flags keep their default")
}

func TestFeaturesMasterSwitchEnablesBetaWebUI(t *testing.T) {
	on := true
	f := FeaturesConfig{Enabled: &on}
	assert.True(t, f.WebUIEnabled(), "experimental master switch turns the beta web UI on")
	assert.True(t, ServerConfig{Features: f}.WebUIEnabled())

	// An explicit web_ui: false beats the master switch.
	off := false
	f2 := FeaturesConfig{Enabled: &on, WebUI: &off}
	assert.False(t, f2.WebUIEnabled())
}

func TestServerConfigWebUIEnabledLegacyCompat(t *testing.T) {
	on, off := true, false

	// Legacy explicit ui.enabled: true keeps the UI on without feature flags.
	sc := ServerConfig{UI: UIServerConfig{Enabled: &on}}
	assert.True(t, sc.WebUIEnabled())

	// Legacy ui.enabled: false forces it off even when the master switch is on.
	sc2 := ServerConfig{UI: UIServerConfig{Enabled: &off}, Features: FeaturesConfig{Enabled: &on}}
	assert.False(t, sc2.WebUIEnabled())

	// Master switch + no legacy field -> on.
	sc3 := ServerConfig{Features: FeaturesConfig{Enabled: &on}}
	assert.True(t, sc3.WebUIEnabled())
}

func TestFeaturesConfigRoundTrip(t *testing.T) {
	off := false
	want := FeaturesConfig{
		Enabled:          &off,
		WebUI:            &off,
		APIRegistration:  &off,
		APIIntrospection: &off,
		APIExposure:      &off,
		Knowledge:        &off,
		Meta:             &off,
		Scripts:          &off,
	}

	yb, err := yaml.Marshal(want)
	require.NoError(t, err)
	var got FeaturesConfig
	require.NoError(t, yaml.Unmarshal(yb, &got))
	assert.Equal(t, want, got)
	assert.False(t, got.WebUIEnabled())

	jb, err := json.Marshal(want)
	require.NoError(t, err)
	var gotJ FeaturesConfig
	require.NoError(t, json.Unmarshal(jb, &gotJ))
	assert.Equal(t, want, gotJ)

	// An empty config round-trips as all-defaults.
	var empty FeaturesConfig
	yb, err = yaml.Marshal(empty)
	require.NoError(t, err)
	require.NotEmpty(t, yb)
	assert.NoError(t, yaml.Unmarshal(yb, &empty))
	assert.False(t, empty.WebUIEnabled())
	assert.True(t, empty.KnowledgeEnabled())
}

func TestServerConfigFeaturesRoundTripYAML(t *testing.T) {
	const doc = `
server:
  port: 9000
  features:
    web_ui: true
    knowledge: false
`
	var fc FileConfig
	require.NoError(t, yaml.Unmarshal([]byte(doc), &fc))
	assert.True(t, fc.Server.WebUIEnabled(), "web_ui:true -> UI served")
	assert.False(t, fc.Server.Features.KnowledgeEnabled())

	// A legacy explicit ui.enabled: false still forces the UI off.
	const doc2 = `
server:
  ui:
    enabled: false
  features:
    web_ui: true
`
	var fc2 FileConfig
	require.NoError(t, yaml.Unmarshal([]byte(doc2), &fc2))
	assert.False(t, fc2.Server.WebUIEnabled(), "legacy ui.enabled:false beats web_ui:true")

	sc := ServerConfig{
		Port: 9000,
		Features: FeaturesConfig{
			Enabled:   boolPtrForTest(true),
			Knowledge: boolPtrForTest(true),
		},
	}
	yb, err := yaml.Marshal(sc)
	require.NoError(t, err)
	var back ServerConfig
	require.NoError(t, yaml.Unmarshal(yb, &back))
	assert.True(t, back.WebUIEnabled())
	assert.True(t, back.Features.KnowledgeEnabled())
}

func boolPtrForTest(v bool) *bool { return &v }
