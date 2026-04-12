package compositor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/patch/agora-os/internal/schema"
)

var defaultViewportActions = []schema.CompositorAccessAction{
	schema.AccessPointer,
	schema.AccessKeyboard,
	schema.AccessReadPixels,
}

type grantStore struct {
	path string
	mu   sync.Mutex
}

func newGrantStore(path string) (*grantStore, error) {
	if path == "" {
		return nil, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	return &grantStore{path: path}, nil
}

func (s *grantStore) Append(record schema.SurfaceGrantRecord) error {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("open grant log: %w", err)
	}
	defer f.Close()

	if err := json.NewEncoder(f).Encode(record); err != nil {
		return fmt.Errorf("append grant log: %w", err)
	}
	return nil
}

func normalizeViewportActions(actions []schema.CompositorAccessAction) []schema.CompositorAccessAction {
	if len(actions) == 0 {
		actions = defaultViewportActions
	}

	seen := make(map[schema.CompositorAccessAction]struct{})
	normalized := make([]schema.CompositorAccessAction, 0, len(actions))
	for _, action := range actions {
		switch action {
		case schema.AccessPointer, schema.AccessKeyboard, schema.AccessReadPixels:
			if _, ok := seen[action]; ok {
				continue
			}
			seen[action] = struct{}{}
			normalized = append(normalized, action)
		}
	}
	sort.Slice(normalized, func(i, j int) bool {
		return normalized[i] < normalized[j]
	})
	return normalized
}

func grantAllows(grant schema.SurfaceAccessGrant, action schema.CompositorAccessAction) bool {
	for _, allowed := range grant.Actions {
		if allowed == action {
			return true
		}
	}
	return false
}

func newGrantRecord(kind schema.SurfaceGrantRecordKind, surfaceID string, agentUID uint32, grantedByUID uint32, actions []schema.CompositorAccessAction) schema.SurfaceGrantRecord {
	return schema.SurfaceGrantRecord{
		Kind:         kind,
		SurfaceID:    surfaceID,
		AgentUID:     agentUID,
		Actions:      normalizeViewportActions(actions),
		GrantedByUID: grantedByUID,
		RecordedAt:   time.Now(),
	}
}
