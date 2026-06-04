package teams

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
	"github.com/nextlevelbuilder/goclaw/internal/config"
)

func TestFormatForTeams(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple text",
			input:    "Hello world",
			expected: "Hello world",
		},
		{
			name:     "code blocks",
			input:    "```go\nfmt.Println()\n```",
			expected: "<pre><code>fmt.Println()</code></pre>",
		},
		{
			name:     "tables",
			input:    "| Col 1 | Col 2 |\n|---|---|\n| val 1 | val 2 |",
			expected: "<pre>| Col 1 | Col 2 |\n|---|---|\n| val 1 | val 2 |</pre>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatForTeams(tt.input)
			if got != tt.expected {
				t.Errorf("FormatForTeams() = %q, expected %q", got, tt.expected)
			}
		})
	}
}

func TestDedupCache(t *testing.T) {
	cache := NewDedupCache(10 * time.Millisecond)

	if cache.Seen("id-1") {
		t.Error("new cache should not have seen id-1")
	}

	cache.Mark("id-1")

	if !cache.Seen("id-1") {
		t.Error("cache should have seen id-1 after marking")
	}

	time.Sleep(15 * time.Millisecond)

	if cache.Seen("id-1") {
		t.Error("cache should have expired id-1 after TTL")
	}
}

func TestBotTokenProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST request, got %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Errorf("expected form-urlencoded content-type, got %s", r.Header.Get("Content-Type"))
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"access_token": "mock-access-token",
			"expires_in": 3600
		}`))
	}))
	defer server.Close()

	// Parse server URL to extract mock endpoint details
	cfg := &config.TeamsConfig{
		AppID:       "mock-app-id",
		AppPassword: "mock-password",
		TenantID:    "mock-tenant",
	}

	provider := NewBotTokenProvider(cfg)
	provider.httpClient = server.Client()

	// Override standard request URL with the mock test server
	// by modifying p.fetchClientSecretToken internally to use server.URL
	ctx := context.Background()
	token, err := provider.GetToken(ctx) // This will call client credentials flow
	// Let's mock provider.fetchClientSecretToken or inject a custom HTTP Client redirecting to local URL
	// Since we use http.Client, let's create a custom round tripper to intercept
	provider.httpClient.Transport = &mockRoundTripper{targetURL: server.URL}

	token, err = provider.GetToken(ctx)
	if err != nil {
		t.Fatalf("GetToken failed: %v", err)
	}

	if token != "mock-access-token" {
		t.Errorf("expected 'mock-access-token', got %q", token)
	}

	// Verify cached token works (doesn't trigger new calls)
	provider.httpClient.Transport = &failRoundTripper{}
	token2, err := provider.GetToken(ctx)
	if err != nil {
		t.Fatalf("GetToken cached failed: %v", err)
	}
	if token2 != "mock-access-token" {
		t.Errorf("expected cached token to be 'mock-access-token', got %q", token2)
	}
}

type mockRoundTripper struct {
	targetURL string
}

func (m *mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// Re-route request to local mock server
	newReq, err := http.NewRequestWithContext(req.Context(), req.Method, m.targetURL, req.Body)
	if err != nil {
		return nil, err
	}
	newReq.Header = req.Header
	return http.DefaultTransport.RoundTrip(newReq)
}

type failRoundTripper struct{}

func (f *failRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("unexpected call to HTTP client")
}

func TestJWKSCacheAndVerification(t *testing.T) {
	// Generate mock RSA key for signing
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate RSA key: %v", err)
	}
	publicKey := &privateKey.PublicKey

	// Build key details for JWKS response
	nB64 := base64.RawURLEncoding.EncodeToString(publicKey.N.Bytes())
	eBytes := big.NewInt(int64(publicKey.E)).Bytes()
	eB64 := base64.RawURLEncoding.EncodeToString(eBytes)

	// Mock JWKS Server
	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fmt.Sprintf(`{
			"keys": [
				{
					"kty": "RSA",
					"use": "sig",
					"kid": "mock-key-id",
					"n": "%s",
					"e": "%s"
				}
			]
		}`, nB64, eB64)))
	}))
	defer jwksServer.Close()

	// Mock OpenID configuration Server
	metaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fmt.Sprintf(`{
			"jwks_uri": "%s"
		}`, jwksServer.URL)))
	}))
	defer metaServer.Close()

	// Initialize JWKS cache and redirect metadata endpoint to local mock server
	jwksCache := NewJWKSCache(10 * time.Minute)
	// We override httpClient default transport or intercept openIDMetaURL
	jwksCache.httpClient.Transport = &metaMockRoundTripper{
		metaURL: metaServer.URL,
		jwksURL: jwksServer.URL,
	}

	ctx := context.Background()

	// Validate JWKS prefetching
	err = jwksCache.Prefetch(ctx)
	if err != nil && !strings.Contains(err.Error(), "prefetch request completed") {
		t.Fatalf("JWKS prefetch failed: %v", err)
	}

	key, err := jwksCache.Get(ctx, "mock-key-id")
	if err != nil {
		t.Fatalf("failed to retrieve key from JWKSCache: %v", err)
	}

	if key.N.Cmp(publicKey.N) != 0 || key.E != publicKey.E {
		t.Error("retrieved public key does not match generated key")
	}

	// Generate a valid JWT token
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, &BotClaims{
		ServiceURL: "https://mock-service.teams.com",
		RegisteredClaims: jwt.RegisteredClaims{
			Audience:  jwt.ClaimStrings{"mock-app-id"},
			Issuer:    "https://api.botframework.com",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
		},
	})
	token.Header["kid"] = "mock-key-id"
	tokenString, err := token.SignedString(privateKey)
	if err != nil {
		t.Fatalf("failed to sign token: %v", err)
	}

	// Mock incoming request
	req, _ := http.NewRequest(http.MethodPost, "https://mybot.com/api/messages", nil)
	req.Header.Set("Authorization", "Bearer "+tokenString)

	claims, err := ValidateInboundRequest(ctx, req, "mock-app-id", jwksCache)
	if err != nil {
		t.Fatalf("ValidateInboundRequest failed: %v", err)
	}

	if claims.ServiceURL != "https://mock-service.teams.com" {
		t.Errorf("expected ServiceURL 'https://mock-service.teams.com', got %q", claims.ServiceURL)
	}

	// Validate audience mismatch fails
	_, err = ValidateInboundRequest(ctx, req, "wrong-app-id", jwksCache)
	if err == nil {
		t.Error("expected validation to fail for mismatched audience")
	}
}

type metaMockRoundTripper struct {
	metaURL string
	jwksURL string
}

func (m *metaMockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	var target string
	if req.URL.String() == openIDMetaURL {
		target = m.metaURL
	} else {
		target = req.URL.String()
	}

	newReq, err := http.NewRequestWithContext(req.Context(), req.Method, target, req.Body)
	if err != nil {
		return nil, err
	}
	newReq.Header = req.Header
	return http.DefaultTransport.RoundTrip(newReq)
}

func TestChannelFactory(t *testing.T) {
	creds := []byte(`{
		"app_id": "test-app-id",
		"app_password": "test-app-password"
	}`)

	cfg := []byte(`{
		"connection_mode": "webhook",
		"webhook_port": 0,
		"dm_policy": "open",
		"group_policy": "open"
	}`)

	ch, err := Factory("test-teams", creds, cfg, nil, nil)
	if err != nil {
		t.Fatalf("Factory instantiation failed: %v", err)
	}

	if ch.Name() != "test-teams" {
		t.Errorf("expected name 'test-teams', got %q", ch.Name())
	}

	if ch.Type() != "teams" {
		t.Errorf("expected type 'teams', got %q", ch.Type())
	}
}

func TestVerifyChannelInterfaces(t *testing.T) {
	cfg := config.TeamsConfig{
		Enabled:     true,
		AppID:       "app-id",
		AppPassword: "app-password",
	}

	ch, err := New(cfg, nil, nil, nil)
	if err != nil {
		t.Fatalf("New channel instantiation failed: %v", err)
	}

	if _, ok := interface{}(ch).(channels.Channel); !ok {
		t.Error("Channel struct does not satisfy channels.Channel interface")
	}

	if _, ok := interface{}(ch).(channels.WebhookChannel); !ok {
		t.Error("Channel struct does not satisfy channels.WebhookChannel interface")
	}
}
