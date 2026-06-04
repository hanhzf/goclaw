package teams

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/nextlevelbuilder/goclaw/internal/config"
)

const (
	openIDMetaURL = "https://login.botframework.com/v1/.well-known/openidconfiguration"
	validAudience = "https://api.botframework.com"
)

// BotClaims represents claims of a token sent by Teams to the bot.
type BotClaims struct {
	ServiceURL string `json:"serviceurl"`
	jwt.RegisteredClaims
}

// JWKSCache caches public keys fetched from Microsoft's JWKS endpoint.
type JWKSCache struct {
	mu         sync.RWMutex
	keys       map[string]*rsa.PublicKey
	fetchedAt  time.Time
	ttl        time.Duration
	httpClient *http.Client
}

// NewJWKSCache creates a new JWKSCache.
func NewJWKSCache(ttl time.Duration) *JWKSCache {
	return &JWKSCache{
		keys:       make(map[string]*rsa.PublicKey),
		ttl:        ttl,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// Prefetch warms up the JWKS cache.
func (c *JWKSCache) Prefetch(ctx context.Context) error {
	_, err := c.refresh(ctx, "")
	if err != nil && !strings.Contains(err.Error(), "unknown kid") {
		return err
	}
	return nil
}

// Get retrieves a public key from the cache, refreshing from the remote JWKS if needed.
func (c *JWKSCache) Get(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	c.mu.RLock()
	key, ok := c.keys[kid]
	age := time.Since(c.fetchedAt)
	c.mu.RUnlock()

	if ok && age < c.ttl {
		return key, nil
	}

	return c.refresh(ctx, kid)
}

func (c *JWKSCache) refresh(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check to avoid concurrent duplicate refreshes
	if key, ok := c.keys[kid]; ok && time.Since(c.fetchedAt) < c.ttl {
		return key, nil
	}

	// Step 1: Fetch metadata configuration
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, openIDMetaURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch openid metadata: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openid metadata returned status %d", resp.StatusCode)
	}

	var meta struct {
		JwksURI string `json:"jwks_uri"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return nil, err
	}

	// Step 2: Fetch JWKS keys
	reqKeys, err := http.NewRequestWithContext(ctx, http.MethodGet, meta.JwksURI, nil)
	if err != nil {
		return nil, err
	}
	respKeys, err := c.httpClient.Do(reqKeys)
	if err != nil {
		return nil, fmt.Errorf("fetch jwks keys: %w", err)
	}
	defer respKeys.Body.Close()

	if respKeys.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jwks keys returned status %d", respKeys.StatusCode)
	}

	var jwks struct {
		Keys []struct {
			Kid string `json:"kid"`
			N   string `json:"n"` // base64url encoded modulus
			E   string `json:"e"` // base64url encoded exponent
		} `json:"keys"`
	}
	if err := json.NewDecoder(respKeys.Body).Decode(&jwks); err != nil {
		return nil, err
	}

	newKeys := make(map[string]*rsa.PublicKey, len(jwks.Keys))
	for _, k := range jwks.Keys {
		pub, err := parseRSAPublicKey(k.N, k.E)
		if err != nil {
			continue // skip malformed keys
		}
		newKeys[k.Kid] = pub
	}

	c.keys = newKeys
	c.fetchedAt = time.Now()

	if kid == "" {
		return nil, errors.New("prefetch request completed")
	}

	key, ok := newKeys[kid]
	if !ok {
		return nil, fmt.Errorf("jwks: unknown kid %q after refresh", kid)
	}
	return key, nil
}

func parseRSAPublicKey(nB64, eB64 string) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(nB64)
	if err != nil {
		return nil, err
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(eB64)
	if err != nil {
		return nil, err
	}

	n := new(big.Int).SetBytes(nBytes)
	var eInt int
	for _, b := range eBytes {
		eInt = (eInt << 8) + int(b)
	}

	return &rsa.PublicKey{N: n, E: eInt}, nil
}

// ValidateInboundRequest validates the JWT in the Authorization header of the request.
func ValidateInboundRequest(ctx context.Context, r *http.Request, appID string, cache *JWKSCache) (*BotClaims, error) {
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		return nil, errors.New("missing bearer token")
	}
	tokenStr := strings.TrimPrefix(authHeader, "Bearer ")

	var claims BotClaims
	token, err := jwt.ParseWithClaims(tokenStr, &claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}

		kid, ok := t.Header["kid"].(string)
		if !ok {
			return nil, errors.New("missing kid in token header")
		}

		return cache.Get(ctx, kid)
	})
	if err != nil {
		return nil, fmt.Errorf("invalid jwt: %w", err)
	}

	if !token.Valid {
		return nil, errors.New("jwt is not valid")
	}

	// Verify audience (should match our AppID or api.botframework.com)
	audMatched := false
	audList, err := claims.GetAudience()
	if err == nil {
		for _, aud := range audList {
			if aud == appID || aud == validAudience {
				audMatched = true
				break
			}
		}
	}
	if !audMatched {
		return nil, fmt.Errorf("jwt aud mismatch: expected %s, got %v", appID, audList)
	}

	// Verify issuer
	iss, err := claims.GetIssuer()
	if err != nil || iss != "https://api.botframework.com" {
		return nil, fmt.Errorf("jwt iss mismatch: got %s", iss)
	}

	return &claims, nil
}

// BotTokenProvider handles fetching and caching the bot's outgoing authentication token.
type BotTokenProvider struct {
	mu          sync.Mutex
	cfg         *config.TeamsConfig
	cachedToken string
	expiresAt   time.Time
	httpClient  *http.Client
}

// NewBotTokenProvider creates a new BotTokenProvider.
func NewBotTokenProvider(cfg *config.TeamsConfig) *BotTokenProvider {
	return &BotTokenProvider{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

// GetToken retrieves a valid access token, refreshing it if expired or near expiration.
func (p *BotTokenProvider) GetToken(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Use token cache if still valid (with 60 seconds buffer)
	if p.cachedToken != "" && time.Now().Add(60*time.Second).Before(p.expiresAt) {
		return p.cachedToken, nil
	}

	var token string
	var expiresIn int64
	var err error

	switch p.cfg.AuthType {
	case "federated":
		// Federated auth cert/managed identity (Placeholder/P2 support)
		return "", errors.New("teams: federated auth not implemented in this phase")
	default:
		token, expiresIn, err = p.fetchClientSecretToken(ctx)
	}

	if err != nil {
		return "", err
	}

	p.cachedToken = token
	p.expiresAt = time.Now().Add(time.Duration(expiresIn) * time.Second)
	return token, nil
}

func (p *BotTokenProvider) fetchClientSecretToken(ctx context.Context) (string, int64, error) {
	tenant := "botframework.com"
	if p.cfg.TenantID != "" {
		tenant = p.cfg.TenantID
	}

	u := fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", tenant)

	data := url.Values{}
	data.Set("grant_type", "client_credentials")
	data.Set("client_id", p.cfg.AppID)
	data.Set("client_secret", p.cfg.AppPassword)
	data.Set("scope", "https://api.botframework.com/.default")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(data.Encode()))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("request token failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", 0, fmt.Errorf("token endpoint returned status %d: %s", resp.StatusCode, string(body))
	}

	var res struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", 0, err
	}

	if res.AccessToken == "" {
		return "", 0, errors.New("empty access token returned")
	}

	return res.AccessToken, res.ExpiresIn, nil
}
