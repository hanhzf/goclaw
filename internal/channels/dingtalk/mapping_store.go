package dingtalk

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/config"
)

// UserMapping represents a single identity translation record.
type UserMapping struct {
	StaffID    string    `json:"staff_id"`
	Mobile     string    `json:"mobile"`
	PersonCode string    `json:"person_code"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// MappingStore handles persistent storage of user identity mappings.
type MappingStore struct {
	filePath string
	mu       sync.Mutex
}

// NewMappingStore creates a new store with the default file path in DataDir.
func NewMappingStore() *MappingStore {
	dataDir := config.ResolvedDataDirFromEnv()
	return &MappingStore{
		filePath: filepath.Join(dataDir, "dingtalk_identity_mappings.json"),
	}
}

// Save records a mapping to the local JSON file.
func (s *MappingStore) Save(mapping UserMapping) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(s.filePath), 0755); err != nil {
		return err
	}

	// Load existing mappings
	mappings := make(map[string]UserMapping)
	if data, err := os.ReadFile(s.filePath); err == nil {
		json.Unmarshal(data, &mappings)
	}

	// Update or add the mapping
	mapping.UpdatedAt = time.Now()
	mappings[mapping.StaffID] = mapping

	// Save back to file
	data, err := json.MarshalIndent(mappings, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(s.filePath, data, 0644)
}

// LoadAll returns all saved mappings.
func (s *MappingStore) LoadAll() (map[string]UserMapping, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]UserMapping), nil
		}
		return nil, err
	}

	mappings := make(map[string]UserMapping)
	if err := json.Unmarshal(data, &mappings); err != nil {
		return nil, err
	}
	return mappings, nil
}
