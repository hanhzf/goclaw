package dingtalk

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/config"
)

// OrgCenterClient handles interactions with the external Organization Center.
type OrgCenterClient struct {
	cfg    config.OrgCenterConfig
	client *http.Client
}

// NewOrgCenterClient creates a new client for the Organization Center.
func NewOrgCenterClient(cfg config.OrgCenterConfig) *OrgCenterClient {
	return &OrgCenterClient{
		cfg: cfg,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// OrgCenterResponse represents the expected JSON response from the Organization Center.
type OrgCenterResponse struct {
	Success bool `json:"success"`
	Data    []struct {
		PersonCode string `json:"personCode"`
		Phone      string `json:"phone"`
		Name       string `json:"userName"`
	} `json:"data"`
	Message string `json:"message"`
}

// LookupPersonCode retrieves the person_code for a given mobile number.
// If Mock mode is enabled, it returns the pre-configured mock_code.
func (c *OrgCenterClient) LookupPersonCode(ctx context.Context, mobile string) (string, error) {
	if !c.cfg.Enabled {
		return "", fmt.Errorf("org center integration is disabled")
	}

	// Handle Mock Mode
	if c.cfg.Mode == "mock" {
		if c.cfg.MockCode == "" {
			return "MOCK_USER_ID", nil
		}
		return c.cfg.MockCode, nil
	}

	// Real API Call
	url := fmt.Sprintf("%s/api/OrgPersonRestApi/listUsers?phone=%s", c.cfg.Endpoint, mobile)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	// Add Auth Headers
	req.Header.Set("Access-Key", c.cfg.AccessKey)
	req.Header.Set("Secret-Key", c.cfg.SecretKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("org center API error (status %d): %s", resp.StatusCode, string(body))
	}

	var ocResp OrgCenterResponse
	if err := json.NewDecoder(resp.Body).Decode(&ocResp); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	if !ocResp.Success {
		return "", fmt.Errorf("org center reported failure: %s", ocResp.Message)
	}

	if len(ocResp.Data) == 0 {
		return "", fmt.Errorf("no user found for mobile: %s", mobile)
	}

	// Return the first match
	return ocResp.Data[0].PersonCode, nil
}
