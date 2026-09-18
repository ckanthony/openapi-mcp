package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ckanthony/openapi-mcp/pkg/config"
	"github.com/ckanthony/openapi-mcp/pkg/mcp"
)

// tokenCacheEntry holds a login-derived session token for an API+target, plus
// the parsed token type (e.g. "Bearer") and its expiry.
type tokenCacheEntry struct {
	token     string
	tokenType string // e.g. "Bearer", "" if unknown
	expiresAt time.Time
}

const (
	// defaultTokenTTL is used when the login response does not carry expires_in.
	defaultTokenTTL = time.Hour
	// DefaultTokenHeader / prefix used when a target does not override them.
	DefaultTokenHeader = "Authorization"
	DefaultTokenPrefix = "Bearer "
)

// InferAuthConfig derives the API-level auth config from the security schemes
// declared in the spec. It picks the most relevant scheme (preferring the one
// the root security requirement references) and maps it to a config.AuthConfig.
func InferAuthConfig(schemes []mcp.SecurityScheme) config.AuthConfig {
	best := config.AuthConfig{}
	bestScore := -1
	for _, ss := range schemes {
		score := 0
		if ss.Required {
			score += 100
		}
		var auth config.AuthConfig
		switch strings.ToLower(ss.Type) {
		case "http":
			switch strings.ToLower(ss.Scheme) {
			case "bearer":
				score += 80
				auth = config.AuthConfig{Type: config.AuthHTTP, HTTPScheme: "bearer"}
			case "basic":
				score += 60
				auth = config.AuthConfig{Type: config.AuthHTTP, HTTPScheme: "basic"}
			default:
				score += 50
				auth = config.AuthConfig{Type: config.AuthHTTP, HTTPScheme: strings.ToLower(ss.Scheme)}
			}
		case "oauth2":
			score += 70
			auth = config.AuthConfig{
				Type:     config.AuthOAuth2,
				Flow:     strings.ToLower(ss.Flow),
				TokenURL: ss.TokenURL,
			}
			if auth.Flow == "" {
				auth.Flow = "authorizationCode"
			}
		case "apikey":
			score += 65
			auth = config.AuthConfig{Type: config.AuthAPIKey, In: ss.In, Name: ss.Name}
		case "openidconnect":
			score += 40
			auth = config.AuthConfig{Type: config.AuthOpenID, TokenURL: ss.TokenURL}
		default:
			continue
		}
		if score > bestScore {
			bestScore = score
			best = auth
		}
	}
	return best.Effective()
}

// loginOperationFor returns the name of the API's login operation: the
// explicitly configured LoginOperation (prefix-tolerant) or, when unset, the
// operation inferred from the spec.
func loginOperationFor(api *apiEntry) string {
	if api == nil {
		return ""
	}
	if op := api.Def.Auth.LoginOperation; op != "" {
		// Accept both the bare operationId and the fully qualified tool name.
		if _, ok := api.ToolSet.Operations[op]; ok {
			return op
		}
		prefix := api.Def.Name + toolNameSep
		if bare := strings.TrimPrefix(op, prefix); bare != op {
			if _, ok := api.ToolSet.Operations[bare]; ok {
				return bare
			}
		}
	}
	return inferLoginOperation(api.ToolSet)
}

// inferLoginOperation heuristically picks the operation used to authenticate
// against the API (e.g. login/sign-in/authenticate endpoints). Returns "" when
// no candidate is found.
func inferLoginOperation(ts *mcp.ToolSet) string {
	if ts == nil || len(ts.Operations) == 0 {
		return ""
	}
	bestName := ""
	bestScore := 0
	for name, op := range ts.Operations {
		lname := strings.ToLower(name)
		lpath := strings.ToLower(op.Path)
		score := 0
		switch {
		case strings.Contains(lname, "login") || strings.Contains(lpath, "/login"):
			score += 4
		case strings.Contains(lname, "signin") || strings.Contains(lname, "sign_in"):
			score += 4
		case strings.Contains(lname, "authenticate"):
			score += 3
		case strings.Contains(lname, "auth") || strings.Contains(lpath, "/auth"):
			score += 2
		}
		// A token-refresh endpoint is not a login endpoint.
		if strings.Contains(lname, "refresh") {
			score -= 2
		}
		if op.Method == http.MethodPost {
			score++
		}
		if score > bestScore {
			bestScore = score
			bestName = name
		}
	}
	if bestScore == 0 {
		return ""
	}
	return bestName
}

// needsLoginOperation reports whether the API's auth scheme relies on calling a
// login *operation* to obtain a session token (as opposed to an OAuth2 token
// exchange, or a static key).
func needsLoginOperation(auth config.AuthConfig) bool {
	switch auth.Type {
	case config.AuthCustomLogin, config.AuthOpenID:
		return true
	case config.AuthHTTP:
		return !strings.EqualFold(auth.HTTPScheme, "basic")
	case config.AuthOAuth2:
		return auth.TokenURL == "" // no token URL → fall back to an operation
	default:
		return false
	}
}

// applyAuthToConfig resolves the credential a target should present for its
// API (per the API-level Auth config) and attaches it to the request Config.
// It supports static API keys, HTTP basic auth, and login-derived session
// tokens (via an OAuth2 token endpoint or a login operation). No-op when the
// API declares no auth or the target holds no matching credentials.
func (r *Registry) applyAuthToConfig(api *apiEntry, target config.TargetDefinition, cfg *config.Config) error {
	auth := api.Def.Auth.Effective()
	if !auth.IsConfigured() {
		return nil
	}

	switch auth.Type {
	case config.AuthAPIKey:
		if key := target.APIKeyValue(); key != "" {
			cfg.APIKey = key
			cfg.APIKeyName = auth.Name
			cfg.APIKeyLocation = config.APIKeyLocation(auth.In)
		}
		return nil

	case config.AuthHTTP:
		if strings.EqualFold(auth.HTTPScheme, "basic") {
			user, pass := target.LoginCredentials()
			if user == "" || pass == "" {
				return nil // no credentials to present
			}
			attachToken(cfg, auth, base64.StdEncoding.EncodeToString([]byte(user+":"+pass)))
			return nil
		}
		// bearer (or other http scheme): a static key acts as a pre-shared
		// token; otherwise authenticate via login/oauth2.
		if key := target.APIKeyValue(); key != "" {
			attachToken(cfg, auth, key)
			return nil
		}
		if target.UsesLogin() {
			entry, err := r.ensureSessionToken(api, target, auth)
			if err != nil {
				return err
			}
			attachToken(cfg, auth, entry.token)
		}
		return nil

	case config.AuthOAuth2:
		if key := target.APIKeyValue(); key != "" {
			attachToken(cfg, auth, key)
			return nil
		}
		if target.UsesLogin() {
			entry, err := r.ensureSessionToken(api, target, auth)
			if err != nil {
				return err
			}
			attachToken(cfg, auth, entry.token)
		}
		return nil

	case config.AuthCustomLogin, config.AuthOpenID:
		if key := target.APIKeyValue(); key != "" {
			attachToken(cfg, auth, key)
			return nil
		}
		if target.UsesLogin() {
			entry, err := r.ensureSessionToken(api, target, auth)
			if err != nil {
				return err
			}
			attachToken(cfg, auth, entry.token)
		}
		return nil
	}
	return nil
}

// attachToken sets the session-token fields on the request Config using the
// API's auth placement (name/location/prefix).
func attachToken(cfg *config.Config, auth config.AuthConfig, token string) {
	cfg.SessionToken = token
	cfg.SessionTokenName = auth.Name
	if loc, err := config.ParseAPIKeyLocation(auth.In); err == nil {
		cfg.SessionTokenLocation = loc
	} else {
		cfg.SessionTokenLocation = config.APIKeyLocationHeader
	}
	cfg.SessionTokenPrefix = auth.Prefix
}

// ensureSessionToken returns a fresh, cached session token for an api+target. It
// authenticates via the API's OAuth2 token endpoint (when the auth scheme has a
// TokenURL) or via the API's login operation otherwise.
func (r *Registry) ensureSessionToken(api *apiEntry, target config.TargetDefinition, auth config.AuthConfig) (*tokenCacheEntry, error) {
	api.tokenMu.Lock()
	defer api.tokenMu.Unlock()

	if cached := api.loginTokens[target.Name]; cached != nil && time.Now().Before(cached.expiresAt) {
		return cached, nil
	}

	var (
		entry *tokenCacheEntry
		err   error
	)
	if auth.Type == config.AuthOAuth2 && auth.TokenURL != "" {
		entry, err = r.exchangeOAuthToken(api, target, auth)
	} else {
		entry, err = r.loginViaOperation(api, target)
	}
	if err != nil {
		return nil, err
	}
	if entry.token == "" {
		return nil, fmt.Errorf("API %q target %q: authentication succeeded but no token was found in the response; expected a 'token'/'access_token' field or an Authorization/X-Auth-Token header", api.Def.Name, target.Name)
	}
	api.loginTokens[target.Name] = entry
	return entry, nil
}

// exchangeOAuthToken performs a standard OAuth2 grant against the API's token
// endpoint. It supports the password flow (username/password) and the
// client_credentials flow (target login creds used as client_id/client_secret).
func (r *Registry) exchangeOAuthToken(api *apiEntry, target config.TargetDefinition, auth config.AuthConfig) (*tokenCacheEntry, error) {
	username, password := target.LoginCredentials()
	if username == "" || password == "" {
		return nil, fmt.Errorf("API %q target %q: OAuth2 %q flow requires client credentials but username/password are not configured (set login_username/login_password or their *_env variants)", api.Def.Name, target.Name, auth.Flow)
	}

	base := target.ToConfig().ServerBaseURL
	if base == "" {
		base = sniffSpecBase(api.ToolSet)
	}
	tokenURL := resolveTokenURL(base, auth.TokenURL)
	if tokenURL == "" {
		return nil, fmt.Errorf("API %q target %q: OAuth2 flow has no usable token URL", api.Def.Name, target.Name)
	}

	form := url.Values{}
	switch strings.ToLower(auth.Flow) {
	case "clientcredentials", "client_credentials":
		form.Set("grant_type", "client_credentials")
		form.Set("client_id", username)
		form.Set("client_secret", password)
	default: // password and everything else
		form.Set("grant_type", "password")
		form.Set("username", username)
		form.Set("password", password)
	}

	client := httpClientForConfig(target.ToConfig())
	httpResp, err := client.PostForm(tokenURL, form)
	if err != nil {
		return nil, fmt.Errorf("API %q target %q: OAuth2 token exchange at %s failed: %w", api.Def.Name, target.Name, tokenURL, err)
	}
	defer httpResp.Body.Close()
	body, _ := io.ReadAll(httpResp.Body)
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return nil, fmt.Errorf("API %q target %q: OAuth2 token exchange at %s failed with status %s: %s", api.Def.Name, target.Name, tokenURL, httpResp.Status, strings.TrimSpace(string(body)))
	}
	return parseLoginResponse(body, httpResp.Header)
}

// resolveTokenURL combines the target base URL and a possibly-relative OAuth
// token URL.
func resolveTokenURL(base, tokenURL string) string {
	if tokenURL == "" {
		return ""
	}
	if strings.HasPrefix(tokenURL, "http://") || strings.HasPrefix(tokenURL, "https://") {
		return tokenURL
	}
	return strings.TrimSuffix(base, "/") + "/" + strings.TrimPrefix(tokenURL, "/")
}

// sniffSpecBase returns a base URL found in any operation of the toolset, used
// as a fallback when resolving a relative OAuth token URL.
func sniffSpecBase(ts *mcp.ToolSet) string {
	for _, op := range ts.Operations {
		if op.BaseURL != "" {
			return op.BaseURL
		}
	}
	return ""
}

// loginViaOperation authenticates by calling the API's login operation (the
// configured or inferred login_operation) with the target's credentials.
func (r *Registry) loginViaOperation(api *apiEntry, target config.TargetDefinition) (*tokenCacheEntry, error) {
	opName := loginOperationFor(api)
	if opName == "" {
		return nil, fmt.Errorf("API %q target %q requires login but no login operation was found in the spec; set the API's 'auth.login_operation' to the operationId that authenticates", api.Def.Name, target.Name)
	}
	username, password := target.LoginCredentials()
	if username == "" || password == "" {
		return nil, fmt.Errorf("API %q target %q requires login credentials but username/password are not configured (set login_username/login_password or their *_env variants)", api.Def.Name, target.Name)
	}
	tool, ok := findTool(api, opName)
	if !ok {
		return nil, fmt.Errorf("API %q: login operation %q not found in the generated tools", api.Def.Name, opName)
	}

	input := buildLoginInput(tool.InputSchema, username, password)
	targetCfg := target.ToConfig()
	params := ToolCallParams{ToolName: opName, Input: input}
	httpResp, err := executeToolCall(&params, api.ToolSet, targetCfg)
	if err != nil {
		return nil, fmt.Errorf("API %q target %q: login via %q failed: %w", api.Def.Name, target.Name, opName, err)
	}
	defer httpResp.Body.Close()

	bodyBytes, _ := io.ReadAll(httpResp.Body)
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return nil, fmt.Errorf("API %q target %q: login via %q failed with status %s: %s", api.Def.Name, target.Name, opName, httpResp.Status, strings.TrimSpace(string(bodyBytes)))
	}
	return parseLoginResponse(bodyBytes, httpResp.Header)
}

func findTool(api *apiEntry, opName string) (mcp.Tool, bool) {
	for _, t := range api.ToolSet.Tools {
		if t.Name == opName {
			return t, true
		}
	}
	return mcp.Tool{}, false
}

// buildLoginInput maps a username/password pair onto the login operation's input
// schema. It looks for properties whose names suggest a user identifier or a
// secret and fills them; if none match it falls back to "username"/"password".
func buildLoginInput(schema mcp.Schema, username, password string) map[string]interface{} {
	input := make(map[string]interface{}, 2)

	userProp, passProp := "", ""
	for name := range schema.Properties {
		lower := strings.ToLower(name)
		if userProp == "" && looksLikeUsername(lower) {
			userProp = name
		}
		if passProp == "" && looksLikePassword(lower) {
			passProp = name
		}
	}
	if userProp != "" {
		input[userProp] = username
	} else if _, fallback := schema.Properties["username"]; fallback {
		input["username"] = username
	} else {
		input["username"] = username // best-effort; executeToolCall may still route it
	}

	if passProp != "" {
		input[passProp] = password
	} else if _, fallback := schema.Properties["password"]; fallback {
		input["password"] = password
	} else {
		input["password"] = password
	}
	return input
}

func looksLikeUsername(s string) bool {
	for _, kw := range []string{"username", "user", "email", "login", "account"} {
		if strings.Contains(s, kw) {
			return true
		}
	}
	return false
}

func looksLikePassword(s string) bool {
	for _, kw := range []string{"password", "pass", "secret", "pwd"} {
		if strings.Contains(s, kw) {
			return true
		}
	}
	return false
}

// parseLoginResponse extracts a token (and optional TTL/token type) from a login
// response, looking first at the JSON body and then at response headers.
func parseLoginResponse(body []byte, headers http.Header) (*tokenCacheEntry, error) {
	entry := &tokenCacheEntry{}

	// 1. JSON body: recursively search for token-like keys.
	var payload interface{}
	if err := json.Unmarshal(body, &payload); err == nil {
		collectToken(payload, entry)
	}

	// 2. Response headers fallback.
	if entry.token == "" {
		for _, h := range []string{"Authorization", "X-Auth-Token", "Set-Cookie"} {
			if v := headers.Get(h); v != "" {
				entry.token = strings.TrimSpace(v)
				entry.token = strings.TrimPrefix(entry.token, "Bearer ")
				if strings.Contains(strings.ToLower(headers.Get(h)), "bearer ") {
					entry.tokenType = "Bearer"
				}
				break
			}
		}
	}

	// expiry defaults to one hour unless the body supplied expires_in.
	if entry.expiresAt.IsZero() {
		entry.expiresAt = time.Now().Add(defaultTokenTTL)
	}
	return entry, nil
}

// collectToken walks parsed JSON and stores the first token-like value found,
// along with any token_type/expires_in values.
func collectToken(node interface{}, entry *tokenCacheEntry) {
	switch v := node.(type) {
	case map[string]interface{}:
		for k, val := range v {
			switch strings.ToLower(k) {
			case "token", "access_token", "accesstoken", "jwt", "auth_token", "id_token", "session_token":
				if s, ok := val.(string); ok && entry.token == "" && s != "" {
					entry.token = s
				}
			case "token_type", "tokentype":
				if s, ok := val.(string); ok {
					entry.tokenType = strings.TrimSpace(s)
				}
			case "expires_in", "expiresin", "expiry":
				if n, ok := toSeconds(val); ok {
					entry.expiresAt = time.Now().Add(time.Duration(n) * time.Second)
				}
			default:
				collectToken(val, entry)
			}
		}
	case []interface{}:
		for _, item := range v {
			collectToken(item, entry)
		}
	}
}

func toSeconds(v interface{}) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int:
		return int64(n), true
	case int64:
		return n, true
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	default:
		return 0, false
	}
}
