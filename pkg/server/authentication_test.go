package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ckanthony/openapi-mcp/pkg/config"
	"github.com/ckanthony/openapi-mcp/pkg/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const loginSpec = `{
  "openapi": "3.0.0",
  "info": {"title": "Login API", "version": "1.0.0"},
  "servers": [{"url": "https://example.com"}],
  "paths": {
    "/login": {
      "post": {
        "operationId": "loginUser",
        "requestBody": {
          "required": true,
          "content": {
            "application/json": {
              "schema": {
                "type": "object",
                "properties": {
                  "username": {"type": "string"},
                  "password": {"type": "string"}
                },
                "required": ["username", "password"]
              }
            }
          }
        },
        "responses": {"200": {"description": "OK"}}
      }
    },
    "/me": {
      "get": {
        "operationId": "getMe",
        "responses": {"200": {"description": "OK"}}
      }
    }
  }
}`

func TestInferLoginOperation(t *testing.T) {
	ts := &mcp.ToolSet{
		Operations: map[string]mcp.OperationDetail{
			"loginUser":    {Method: "POST", Path: "/login"},
			"getMe":        {Method: "GET", Path: "/me"},
			"refreshToken": {Method: "POST", Path: "/token/refresh"},
		},
	}
	assert.Equal(t, "loginUser", inferLoginOperation(ts))
}

func TestInferLoginOperationNone(t *testing.T) {
	ts := &mcp.ToolSet{
		Operations: map[string]mcp.OperationDetail{
			"getMe": {Method: "GET", Path: "/me"},
		},
	}
	assert.Equal(t, "", inferLoginOperation(ts))
}

func TestLoginOperationPrecedence(t *testing.T) {
	reg := NewRegistry("")
	_, err := reg.RegisterAPI(exposedAPI(config.APIDefinition{
		Name: "loginapi",
		Spec: loginSpec,
		Auth: config.AuthConfig{
			Type:           config.AuthCustomLogin,
			LoginOperation: "getMe", // explicitly wrong on purpose
		},
	}), false)
	require.NoError(t, err)
	api, _, _ := reg.ResolveTool("loginapi__getMe")
	require.NotNil(t, api)
	assert.Equal(t, "getMe", loginOperationFor(api))
}

func TestParseLoginResponse(t *testing.T) {
	// access_token + expires_in
	entry, err := parseLoginResponse([]byte(`{"access_token":"abc123","token_type":"Bearer","expires_in":3600}`), nil)
	require.NoError(t, err)
	assert.Equal(t, "abc123", entry.token)
	assert.Equal(t, "Bearer", entry.tokenType)
	assert.Greater(t, entry.expiresAt.Unix(), int64(0))
	assert.WithinDuration(t, time.Now().Add(time.Hour), entry.expiresAt, 5*time.Second)
}

func TestParseLoginResponseNested(t *testing.T) {
	entry, err := parseLoginResponse([]byte(`{"data":{"session":{"token":"tok-7"},"expires_in":600}}`), nil)
	require.NoError(t, err)
	assert.Equal(t, "tok-7", entry.token)
	assert.WithinDuration(t, time.Now().Add(10*time.Minute), entry.expiresAt, 5*time.Second)
}

func TestParseLoginResponseHeaderFallback(t *testing.T) {
	h := http.Header{}
	h.Set("X-Auth-Token", "headertok")
	entry, err := parseLoginResponse([]byte(`{"status":"ok"}`), h)
	require.NoError(t, err)
	assert.Equal(t, "headertok", entry.token)
}

func TestParseLoginResponseExpiryDefault(t *testing.T) {
	entry, err := parseLoginResponse([]byte(`{"token":"xyz"}`), nil)
	require.NoError(t, err)
	assert.Equal(t, "xyz", entry.token)
	// default 1h
	assert.InDelta(t, 3600, entry.expiresAt.Sub(time.Now()).Seconds(), 5)
}

func TestBuildLoginInput(t *testing.T) {
	schema := mcp.Schema{
		Type: "object",
		Properties: map[string]mcp.Schema{
			"username": {Type: "string"},
			"password": {Type: "string"},
		},
	}
	input := buildLoginInput(schema, "alice", "s3cret")
	assert.Equal(t, map[string]interface{}{"username": "alice", "password": "s3cret"}, input)
}

func TestBuildLoginInputImplicitMapping(t *testing.T) {
	// Fields with non-obvious names map via heuristics.
	schema := mcp.Schema{
		Type: "object",
		Properties: map[string]mcp.Schema{
			"user": {Type: "string"},
			"pass": {Type: "string"},
		},
	}
	input := buildLoginInput(schema, "bob", "p4ss")
	assert.Equal(t, map[string]interface{}{"user": "bob", "pass": "p4ss"}, input)
}

// --- End-to-end login flow against an httptest backend ---

func TestLoginFlowExecutesAndInjectsToken(t *testing.T) {
	var loginCalls atomic.Int32
	var meHeaders atomic.Value

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			loginCalls.Add(1)
			var body map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				w.WriteHeader(400)
				return
			}
			// Verify the submitted credentials reached the login endpoint.
			if body["username"] != "alice" || body["password"] != "s3cret" {
				w.WriteHeader(401)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"access_token":"<TOKEN>","token_type":"Bearer","expires_in":3600}`)
		case "/me":
			meHeaders.Store(r.Header.Get("Authorization"))
			fmt.Fprint(w, `{"ok":true}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer backend.Close()

	reg := NewRegistry("")
	_, err := reg.RegisterAPI(exposedAPI(config.APIDefinition{
		Name: "loginapi",
		Spec: loginSpec,
		Auth: config.AuthConfig{Type: config.AuthCustomLogin},
		Targets: []config.TargetDefinition{
			{Name: "default", BaseURL: backend.URL, LoginUsername: "alice", LoginPassword: "s3cret"},
		},
	}), false)
	require.NoError(t, err)

	httpResp, err := executeRegisteredTool(reg, "", &ToolCallParams{
		ToolName: "loginapi__getMe",
		Input:    map[string]interface{}{},
	})
	require.NoError(t, err)
	defer httpResp.Body.Close()
	assert.Equal(t, 200, httpResp.StatusCode)

	assert.Equal(t, int32(1), loginCalls.Load(), "login should be performed exactly once (cached)")
	assert.Equal(t, "Bearer <TOKEN>", meHeaders.Load().(string))
}

func TestLoginFlowTokenCachedAcrossCalls(t *testing.T) {
	var loginCalls atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			loginCalls.Add(1)
			_ = json.NewDecoder(r.Body).Decode(&map[string]interface{}{})
			fmt.Fprint(w, `{"access_token":"T2","token_type":"Bearer"}`)
			return
		}
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer backend.Close()

	reg := NewRegistry("")
	reg.RegisterAPI(exposedAPI(config.APIDefinition{
		Name: "loginapi",
		Spec: loginSpec,
		Auth: config.AuthConfig{Type: config.AuthCustomLogin},
		Targets: []config.TargetDefinition{
			{Name: "default", BaseURL: backend.URL, LoginUsernameEnv: "TL_USER", LoginPasswordEnv: "TL_PASS"},
		},
	}), false)
	t.Setenv("TL_USER", "alice")
	t.Setenv("TL_PASS", "s3cret")

	for i := 0; i < 3; i++ {
		_, err := executeRegisteredTool(reg, "", &ToolCallParams{
			ToolName: "loginapi__getMe",
			Input:    map[string]interface{}{},
		})
		require.NoError(t, err)
	}
	assert.Equal(t, int32(1), loginCalls.Load(), "token must be cached, not re-login on every call")
}

func TestLoginOperationMissingErrors(t *testing.T) {
	spec := `{
	  "openapi": "3.0.0",
	  "info": {"title": "No login API", "version": "1"},
	  "paths": {
	    "/ping": {"get": {"operationId": "ping", "responses": {"200": {"description": "OK"}}}}
	  }
	}`
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{}`)
	}))
	defer backend.Close()

	reg := NewRegistry("")
	reg.RegisterAPI(exposedAPI(config.APIDefinition{
		Name: "nologin",
		Spec: spec,
		Auth: config.AuthConfig{Type: config.AuthCustomLogin},
		Targets: []config.TargetDefinition{
			{Name: "default", BaseURL: backend.URL, LoginUsername: "u", LoginPassword: "p"},
		},
	}), false)

	_, err := executeRegisteredTool(reg, "", &ToolCallParams{ToolName: "nologin__ping", Input: map[string]interface{}{}})
	assert.ErrorContains(t, err, "no login operation")
	assert.ErrorContains(t, err, "login_operation")
}

func TestLoginTokenNotInResponseErrors(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			fmt.Fprint(w, `{"status":"ok"}`) // no token field
			return
		}
		fmt.Fprint(w, `{}`)
	}))
	defer backend.Close()

	reg := NewRegistry("")
	reg.RegisterAPI(exposedAPI(config.APIDefinition{
		Name: "loginapi",
		Spec: loginSpec,
		Auth: config.AuthConfig{Type: config.AuthCustomLogin},
		Targets: []config.TargetDefinition{
			{Name: "default", BaseURL: backend.URL, LoginUsername: "u", LoginPassword: "p"},
		},
	}), false)

	_, err := executeRegisteredTool(reg, "", &ToolCallParams{ToolName: "loginapi__getMe", Input: map[string]interface{}{}})
	assert.ErrorContains(t, err, "no token was found")
}

func TestLoginMissingCredentialsErrors(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{}`)
	}))
	defer backend.Close()

	reg := NewRegistry("")
	reg.RegisterAPI(exposedAPI(config.APIDefinition{
		Name: "loginapi",
		Spec: loginSpec,
		Auth: config.AuthConfig{Type: config.AuthCustomLogin},
		Targets: []config.TargetDefinition{
			// UsesLogin() true (env configured) but env not set -> empty creds.
			{Name: "default", BaseURL: backend.URL, LoginUsernameEnv: "UNSET_USER", LoginPasswordEnv: "UNSET_PASS"},
		},
	}), false)

	_, err := executeRegisteredTool(reg, "", &ToolCallParams{ToolName: "loginapi__getMe", Input: map[string]interface{}{}})
	assert.ErrorContains(t, err, "login credentials")
}

func TestLoginTokenAttachmentLocations(t *testing.T) {
	var loginCalls atomic.Int32
	var receivedQuery, receivedCookie atomic.Value
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			loginCalls.Add(1)
			_ = json.NewDecoder(r.Body).Decode(&map[string]interface{}{})
			fmt.Fprint(w, `{"access_token":"TOK","token_type":"Bearer"}`)
			return
		}
		receivedQuery.Store(r.URL.Query().Get("session"))
		if c, err := r.Cookie("session"); err == nil {
			receivedCookie.Store(c.Value)
		}
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer backend.Close()

	// Query location.
	reg := NewRegistry("")
	reg.RegisterAPI(exposedAPI(config.APIDefinition{
		Name: "loginapi",
		Spec: loginSpec,
		Auth: config.AuthConfig{Type: config.AuthCustomLogin, Name: "session", In: "query"},
		Targets: []config.TargetDefinition{
			{Name: "default", BaseURL: backend.URL, LoginUsername: "u", LoginPassword: "p"},
		},
	}), false)
	_, err := executeRegisteredTool(reg, "", &ToolCallParams{ToolName: "loginapi__getMe", Input: map[string]interface{}{}})
	require.NoError(t, err)
	assert.Equal(t, "TOK", receivedQuery.Load())

	// Cookie location (fresh registry so login re-runs).
	reg = NewRegistry("")
	reg.RegisterAPI(exposedAPI(config.APIDefinition{
		Name: "loginapi",
		Spec: loginSpec,
		Auth: config.AuthConfig{Type: config.AuthCustomLogin, Name: "session", In: "cookie"},
		Targets: []config.TargetDefinition{
			{Name: "default", BaseURL: backend.URL, LoginUsername: "u", LoginPassword: "p"},
		},
	}), false)
	_, err = executeRegisteredTool(reg, "", &ToolCallParams{ToolName: "loginapi__getMe", Input: map[string]interface{}{}})
	require.NoError(t, err)
	assert.Equal(t, "TOK", receivedCookie.Load())
}

// --- Auth inference from spec security schemes ---

func TestInferAuthConfigAPIKey(t *testing.T) {
	auth := InferAuthConfig([]mcp.SecurityScheme{
		{Key: "ApiKeyAuth", Type: "apiKey", In: "query", Name: "api_key", Required: true},
	})
	assert.Equal(t, config.AuthAPIKey, auth.Type)
	assert.Equal(t, "query", auth.In)
	assert.Equal(t, "api_key", auth.Name)
}

func TestInferAuthConfigHTTPBearer(t *testing.T) {
	auth := InferAuthConfig([]mcp.SecurityScheme{
		{Key: "BearerAuth", Type: "http", Scheme: "bearer", Required: true},
	})
	assert.Equal(t, config.AuthHTTP, auth.Type)
	assert.Equal(t, "bearer", auth.HTTPScheme)
	eff := auth.Effective()
	assert.Equal(t, "Authorization", eff.Name)
	assert.Equal(t, "Bearer ", eff.Prefix)
}

func TestInferAuthConfigHTTPBasic(t *testing.T) {
	auth := InferAuthConfig([]mcp.SecurityScheme{
		{Key: "BasicAuth", Type: "http", Scheme: "basic", Required: true},
	})
	assert.Equal(t, config.AuthHTTP, auth.Type)
	assert.Equal(t, "basic", auth.HTTPScheme)
	assert.Equal(t, "Basic ", auth.Effective().Prefix)
}

func TestInferAuthConfigOAuth2Password(t *testing.T) {
	auth := InferAuthConfig([]mcp.SecurityScheme{
		{Key: "OAuth", Type: "oauth2", Flow: "password", TokenURL: "https://example.com/oauth/token", Required: true},
	})
	assert.Equal(t, config.AuthOAuth2, auth.Type)
	assert.Equal(t, "password", auth.Flow)
	assert.Equal(t, "https://example.com/oauth/token", auth.TokenURL)
}

func TestInferAuthConfigPrefersRequired(t *testing.T) {
	auth := InferAuthConfig([]mcp.SecurityScheme{
		{Key: "optional", Type: "http", Scheme: "bearer"},
		{Key: "required", Type: "apiKey", In: "header", Name: "X-Key", Required: true},
	})
	assert.Equal(t, config.AuthAPIKey, auth.Type)
	assert.Equal(t, "X-Key", auth.Name)
}

func TestInferAuthConfigNone(t *testing.T) {
	auth := InferAuthConfig(nil)
	assert.False(t, auth.IsConfigured())
}

// --- API-level apiKey injection end-to-end ---

func TestAPIKeyInjectionAtAPIAuth(t *testing.T) {
	var gotHdr, gotQuery atomic.Value
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHdr.Store(r.Header.Get("X-API-Key"))
		gotQuery.Store(r.URL.Query().Get("key"))
		fmt.Fprint(w, `{}`)
	}))
	defer backend.Close()

	// Header placement (from API-level Auth).
	reg := NewRegistry("")
	reg.RegisterAPI(exposedAPI(config.APIDefinition{
		Name: "keyed",
		Spec: registryTestV3Spec,
		Auth: config.AuthConfig{Type: config.AuthAPIKey, In: "header", Name: "X-API-Key"},
		Targets: []config.TargetDefinition{
			{Name: "default", BaseURL: backend.URL, APIKey: "k-hdr"},
		},
	}), false)
	_, err := executeRegisteredTool(reg, "", &ToolCallParams{ToolName: "keyed__getCurrent", Input: map[string]interface{}{}})
	require.NoError(t, err)
	assert.Equal(t, "k-hdr", gotHdr.Load())

	// Query placement.
	reg = NewRegistry("")
	reg.RegisterAPI(exposedAPI(config.APIDefinition{
		Name: "keyed",
		Spec: registryTestV3Spec,
		Auth: config.AuthConfig{Type: config.AuthAPIKey, In: "query", Name: "key"},
		Targets: []config.TargetDefinition{
			{Name: "default", BaseURL: backend.URL, APIKey: "k-query"},
		},
	}), false)
	_, err = executeRegisteredTool(reg, "", &ToolCallParams{ToolName: "keyed__getCurrent", Input: map[string]interface{}{}})
	require.NoError(t, err)
	assert.Equal(t, "k-query", gotQuery.Load())
}

// --- HTTP basic auth ---

func TestHTTPBasicAuth(t *testing.T) {
	var gotHdr atomic.Value
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHdr.Store(r.Header.Get("Authorization"))
		fmt.Fprint(w, `{}`)
	}))
	defer backend.Close()

	reg := NewRegistry("")
	reg.RegisterAPI(exposedAPI(config.APIDefinition{
		Name: "basic",
		Spec: registryTestV3Spec,
		Auth: config.AuthConfig{Type: config.AuthHTTP, HTTPScheme: "basic"},
		Targets: []config.TargetDefinition{
			{Name: "default", BaseURL: backend.URL, LoginUsername: "user1", LoginPassword: "pass1"},
		},
	}), false)
	_, err := executeRegisteredTool(reg, "", &ToolCallParams{ToolName: "basic__getCurrent", Input: map[string]interface{}{}})
	require.NoError(t, err)
	assert.Equal(t, "Basic dXNlcjE6cGFzczE=", gotHdr.Load()) // base64("user1:pass1")
}

// --- OAuth2 password flow end-to-end ---

func TestOAuth2PasswordFlow(t *testing.T) {
	var tokenPost atomic.Value
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			_ = r.ParseForm()
			tokenPost.Store(map[string]string{
				"grant_type": r.Form.Get("grant_type"),
				"username":   r.Form.Get("username"),
				"password":   r.Form.Get("password"),
			})
			fmt.Fprint(w, `{"access_token":"OAUTH-TOK","token_type":"Bearer","expires_in":3600}`)
			return
		}
		fmt.Fprint(w, `{}`)
	}))
	defer backend.Close()

	// API-level auth: oauth2 password with a token URL.
	reg := NewRegistry("")
	reg.RegisterAPI(exposedAPI(config.APIDefinition{
		Name: "oauth",
		Spec: registryTestV3Spec,
		Auth: config.AuthConfig{
			Type:     config.AuthOAuth2,
			Flow:     "password",
			TokenURL: "/oauth/token",
			In:       "header",
		},
		Targets: []config.TargetDefinition{
			{Name: "default", BaseURL: backend.URL, LoginUsername: "alice", LoginPassword: "secret"},
		},
	}), false)

	_, err := executeRegisteredTool(reg, "", &ToolCallParams{ToolName: "oauth__getCurrent", Input: map[string]interface{}{}})
	require.NoError(t, err)
	posted := tokenPost.Load().(map[string]string)
	assert.Equal(t, "password", posted["grant_type"])
	assert.Equal(t, "alice", posted["username"])
	assert.Equal(t, "secret", posted["password"])

	// The token is cached: a second call does not redo the exchange.
	_, err = executeRegisteredTool(reg, "", &ToolCallParams{ToolName: "oauth__getCurrent", Input: map[string]interface{}{}})
	require.NoError(t, err)
	assert.True(t, true)
}

func TestNeedsLoginOperation(t *testing.T) {
	assert.True(t, needsLoginOperation(config.AuthConfig{Type: config.AuthCustomLogin}))
	assert.True(t, needsLoginOperation(config.AuthConfig{Type: config.AuthHTTP}))
	assert.False(t, needsLoginOperation(config.AuthConfig{Type: config.AuthHTTP, HTTPScheme: "basic"}))
	assert.False(t, needsLoginOperation(config.AuthConfig{Type: config.AuthOAuth2, TokenURL: "https://x/token"}))
	assert.True(t, needsLoginOperation(config.AuthConfig{Type: config.AuthOAuth2})) // no token URL
}

// TestInsecureSkipVerifyTLS verifies that a target served over a self-signed
// TLS endpoint is reachable only when insecure_skip_verify is enabled on the
// target, and that the flag propagates through the runtime config to the HTTP
// client used for tool calls (including the login flow).
func TestInsecureSkipVerifyTLS(t *testing.T) {
	var meHeaders atomic.Value
	backend := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"access_token":"TLS-TOK","token_type":"Bearer"}`)
			return
		}
		meHeaders.Store(r.Header.Get("Authorization"))
		fmt.Fprint(w, `{"ok":true}`)
	}))
	backend.StartTLS() // self-signed cert; does not validate without the flag
	defer backend.Close()

	// 1) Without insecure_skip_verify the call must fail (TLS handshake error).
	reg := NewRegistry("")
	reg.RegisterAPI(exposedAPI(config.APIDefinition{
		Name: "tlsapi",
		Spec: loginSpec,
		Auth: config.AuthConfig{Type: config.AuthCustomLogin},
		Targets: []config.TargetDefinition{
			{Name: "default", BaseURL: backend.URL, LoginUsername: "u", LoginPassword: "p"},
		},
	}), false)
	_, err := executeRegisteredTool(reg, "", &ToolCallParams{ToolName: "tlsapi__getMe", Input: map[string]interface{}{}})
	require.Error(t, err, "call against self-signed TLS must fail without insecure_skip_verify")

	// 2) With insecure_skip_verify the call succeeds.
	reg2 := NewRegistry("")
	reg2.RegisterAPI(exposedAPI(config.APIDefinition{
		Name: "tlsapi",
		Spec: loginSpec,
		Auth: config.AuthConfig{Type: config.AuthCustomLogin},
		Targets: []config.TargetDefinition{
			{Name: "default", BaseURL: backend.URL, LoginUsername: "u", LoginPassword: "p", InsecureSkipVerify: true},
		},
	}), false)
	httpResp, err := executeRegisteredTool(reg2, "", &ToolCallParams{ToolName: "tlsapi__getMe", Input: map[string]interface{}{}})
	require.NoError(t, err)
	defer httpResp.Body.Close()
	assert.Equal(t, 200, httpResp.StatusCode)
	assert.Equal(t, "Bearer TLS-TOK", meHeaders.Load().(string), "login token must be injected")
}
