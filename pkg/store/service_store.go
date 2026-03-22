package store

import (
	"context"
	"fmt"
	"time"
)

// ServiceRecord is the canonical record for an onboarded service.
// Stored at /shipyard/services/{name}.
type ServiceRecord struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	ContextDir  string   `json:"contextDir"`
	Modes       []string `json:"modes"`
	Source      string   `json:"source"`   // "github" | "zip" | "local"
	RepoURL     string   `json:"repoURL"`
	Branch      string   `json:"branch"`
	Engine      string   `json:"engine"`
	OnboardedAt time.Time `json:"onboardedAt"`
}

// PutService writes a service record to etcd.
func (s *Store) PutService(ctx context.Context, record *ServiceRecord) error {
	if record.OnboardedAt.IsZero() {
		record.OnboardedAt = time.Now()
	}
	return s.put(ctx, serviceKey(record.Name), record, 0)
}

// GetService retrieves a service record by name.
// Returns (nil, nil) if not found.
func (s *Store) GetService(ctx context.Context, name string) (*ServiceRecord, error) {
	var record ServiceRecord
	found, err := s.get(ctx, serviceKey(name), &record)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	return &record, nil
}

// ListServices returns all onboarded service records.
func (s *Store) ListServices(ctx context.Context) ([]*ServiceRecord, error) {
	var records []*ServiceRecord
	err := s.list(ctx, prefixServices, func(key string, raw []byte) error {
		var r ServiceRecord
		if err := unmarshalJSON(raw, &r); err != nil {
			fmt.Printf("store: skipping malformed service record at %q: %v\n", key, err)
			return nil
		}
		records = append(records, &r)
		return nil
	})
	return records, err
}

// DeleteService removes a service record from etcd.
func (s *Store) DeleteService(ctx context.Context, name string) error {
	return s.del(ctx, serviceKey(name))
}

// ServiceExists returns true if a service with the given name exists.
func (s *Store) ServiceExists(ctx context.Context, name string) (bool, error) {
	rec, err := s.GetService(ctx, name)
	return rec != nil, err
}
