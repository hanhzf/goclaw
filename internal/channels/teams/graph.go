package teams

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/config"
)

const (
	graphBaseURL = "https://graph.microsoft.com/v1.0"
)

// GraphClient coordinates outgoing queries to Microsoft Graph API.
type GraphClient struct {
	mu          sync.Mutex
	cfg         *config.TeamsConfig
	cachedToken string
	expiresAt   time.Time
	httpClient  *http.Client
}

// NewGraphClient creates a new GraphClient.
func NewGraphClient(cfg *config.TeamsConfig) *GraphClient {
	return &GraphClient{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// GetToken retrieves a token scoped for Microsoft Graph API.
func (g *GraphClient) GetToken(ctx context.Context) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.cachedToken != "" && time.Now().Add(60*time.Second).Before(g.expiresAt) {
		return g.cachedToken, nil
	}

	token, expiresIn, err := g.fetchToken(ctx)
	if err != nil {
		return "", err
	}

	g.cachedToken = token
	g.expiresAt = time.Now().Add(time.Duration(expiresIn) * time.Second)
	return token, nil
}

func (g *GraphClient) fetchToken(ctx context.Context) (string, int64, error) {
	tenant := "common"
	if g.cfg.TenantID != "" {
		tenant = g.cfg.TenantID
	}

	u := fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", tenant)

	data := url.Values{}
	data.Set("grant_type", "client_credentials")
	data.Set("client_id", g.cfg.AppID)
	data.Set("client_secret", g.cfg.AppPassword)
	data.Set("scope", "https://graph.microsoft.com/.default")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(data.Encode()))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("request graph token failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", 0, fmt.Errorf("graph token endpoint returned status %d: %s", resp.StatusCode, string(body))
	}

	var res struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", 0, err
	}

	if res.AccessToken == "" {
		return "", 0, errors.New("empty graph access token returned")
	}

	return res.AccessToken, res.ExpiresIn, nil
}

type graphPage[T any] struct {
	Values   []T    `json:"value"`
	NextLink string `json:"@odata.nextLink"`
}

// fetchAllPages iteratively fetches OData page links until limit is hit or all pages are fetched.
func fetchAllPages[T any](ctx context.Context, g *GraphClient, startURL string, limit int) ([]T, error) {
	var all []T
	u := startURL

	for u != "" {
		if limit > 0 && len(all) >= limit {
			break
		}

		var page graphPage[T]
		if err := g.getJSON(ctx, u, &page); err != nil {
			return all, err
		}

		all = append(all, page.Values...)
		u = page.NextLink

		if u != "" {
			// Small safety sleep to avoid hitting Graph rate limits
			select {
			case <-ctx.Done():
				return all, ctx.Err()
			case <-time.After(50 * time.Millisecond):
			}
		}
	}

	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

func (g *GraphClient) getJSON(ctx context.Context, u string, out any) error {
	const maxRetries = 3

	for attempt := 0; attempt < maxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return err
		}

		token, err := g.GetToken(ctx)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")

		resp, err := g.httpClient.Do(req)
		if err != nil {
			return err
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			retryAfterHeader := resp.Header.Get("Retry-After")
			retryAfter := 5 * time.Second
			if retryAfterHeader != "" {
				if secs, err := strconv.Atoi(retryAfterHeader); err == nil {
					retryAfter = time.Duration(secs) * time.Second
				}
			}
			slog.Warn("graph api throttled, retrying", "url", u, "retry_after", retryAfter, "attempt", attempt+1)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(retryAfter):
				continue
			}
		}

		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			body, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("graph get status %d: %s", resp.StatusCode, string(body))
		}

		return json.NewDecoder(resp.Body).Decode(out)
	}

	return fmt.Errorf("graph api: max retries exceeded for %s", u)
}

// GraphMessage maps Microsoft Graph chat/channel message JSON values.
type GraphMessage struct {
	ID              string    `json:"id"`
	CreatedDateTime time.Time `json:"createdDateTime"`
	From            struct {
		User *struct {
			DisplayName string `json:"displayName"`
			ID          string `json:"id"`
		} `json:"user"`
	} `json:"from"`
	Body struct {
		ContentType string `json:"contentType"`
		Content     string `json:"content"`
	} `json:"body"`
	Attachments []struct {
		ContentType string `json:"contentType"`
		ContentURL  string `json:"contentUrl"`
		Name        string `json:"name"`
	} `json:"attachments"`
	ReplyToID string `json:"replyToId"`
}

// GetChannelHistory loads channel chat message logs using Graph API.
func (g *GraphClient) GetChannelHistory(ctx context.Context, teamID, channelID string, limit int) ([]GraphMessage, error) {
	if limit <= 0 {
		return nil, nil
	}
	pageSize := limit
	if pageSize > 50 {
		pageSize = 50
	}
	// OData ordering is descending so we get newest messages first
	u := fmt.Sprintf("%s/teams/%s/channels/%s/messages?$top=%d&$orderby=createdDateTime desc", graphBaseURL, teamID, channelID, pageSize)
	msgs, err := fetchAllPages[GraphMessage](ctx, g, u, limit)
	if err != nil {
		return nil, err
	}

	// Reverse to ascending order (oldest to newest) to match context thread logs flow
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, nil
}

// GraphMember maps group/team member JSON details.
type GraphMember struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Mail        string `json:"mail"`
}

// GetTeamMembers retrieves the full list of members in a Team/Group.
func (g *GraphClient) GetTeamMembers(ctx context.Context, teamID string) ([]GraphMember, error) {
	u := fmt.Sprintf("%s/groups/%s/members?$select=id,displayName,mail&$top=100", graphBaseURL, teamID)
	return fetchAllPages[GraphMember](ctx, g, u, 0)
}
