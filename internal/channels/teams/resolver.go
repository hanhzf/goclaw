package teams

import (
	"context"
	"log/slog"
	"sync"
)

// UserResolver maps AAD Object IDs (GUIDs) to Outlook email addresses.
type UserResolver struct {
	staticMap map[string]string // from config.user_map
	cache     sync.Map          // runtime cache: AAD ObjectID -> email
	graph     *GraphClient      // optional graph client for dynamic user query
}

// NewUserResolver creates a new UserResolver.
func NewUserResolver(staticMap map[string]string, graph *GraphClient) *UserResolver {
	return &UserResolver{
		staticMap: staticMap,
		graph:     graph,
	}
}

// Resolve translates an AAD Object ID (GUID) into an email address.
// If the mapping is not configured, it tries the runtime cache,
// then the Microsoft Graph API if configured.
// If all resolution paths fail, it logs a warning and returns the displayName as fallback.
func (r *UserResolver) Resolve(ctx context.Context, aadObjectID, displayName string) string {
	if aadObjectID == "" {
		if displayName != "" {
			return displayName
		}
		return "unknown"
	}

	// 1. Check static configuration
	if email, ok := r.staticMap[aadObjectID]; ok && email != "" {
		return email
	}

	// 2. Check runtime cache
	if cachedVal, ok := r.cache.Load(aadObjectID); ok {
		if email, ok := cachedVal.(string); ok && email != "" {
			return email
		}
	}

	// 3. Query Graph API if available
	if r.graph != nil {
		email, err := r.graph.GetUserEmail(ctx, aadObjectID)
		if err == nil && email != "" {
			r.Learn(aadObjectID, email)
			slog.Info("teams: dynamically resolved user via Graph API", "aad_object_id", aadObjectID, "email", email)
			return email
		}
		if err != nil {
			slog.Debug("teams: failed to resolve user via Graph API", "aad_object_id", aadObjectID, "error", err)
		}
	}

	// 4. Fallback to displayName and log warning
	fallback := displayName
	if fallback == "" {
		fallback = aadObjectID
	}

	slog.Warn("teams user not resolved, falling back to display name",
		"aad_object_id", aadObjectID,
		"display_name", displayName,
	)

	return fallback
}

// Learn dynamically populates/updates the runtime cache for an AAD Object ID mapping.
func (r *UserResolver) Learn(aadObjectID, email string) {
	if aadObjectID == "" || email == "" {
		return
	}
	r.cache.Store(aadObjectID, email)
}
