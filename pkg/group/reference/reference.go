// Package reference declares the adapter contract used to count and list the
// rows that still point at a group. Adding a new table that stores a group key
// means adding one adapter, not editing rename logic or archive checks.
package reference

import (
	"context"

	"github.com/QuantumNous/new-api/pkg/group/domain"
)

// Kind separates references that must block archiving from historical ones.
type Kind string

const (
	// KindHard references gate archiving: users.group, tokens.group and
	// channels.group are live routing inputs, so archiving a group they use
	// would break running traffic.
	KindHard Kind = "hard"
	// KindSoft references are historical: logs, quota rollups and metric
	// buckets keep working after archiving and never block it.
	KindSoft Kind = "soft"
)

// Row is one referencing record.
type Row struct {
	Table string
	Id    int64
	Label string
}

// Adapter counts and lists references for one table.
type Adapter interface {
	Name() string
	Kind() Kind
	Count(ctx context.Context, key string) (int64, error)
	List(ctx context.Context, key string, limit, offset int) ([]Row, error)
}

// Registry is the set of known adapters.
type Registry struct {
	adapters []Adapter
}

func NewRegistry(adapters ...Adapter) *Registry {
	return &Registry{adapters: adapters}
}

// Register adds one adapter (used by the model wiring to append optional ones).
func (r *Registry) Register(adapter Adapter) {
	r.adapters = append(r.adapters, adapter)
}

// Counts returns per-table counts, keyed by adapter name.
func (r *Registry) Counts(ctx context.Context, key string) (map[string]int64, error) {
	result := make(map[string]int64, len(r.adapters))
	for _, adapter := range r.adapters {
		count, err := adapter.Count(ctx, key)
		if err != nil {
			return nil, err
		}
		result[adapter.Name()] = count
	}
	return result, nil
}

// HardReferenceCount sums every hard adapter's count.
func (r *Registry) HardReferenceCount(ctx context.Context, key string) (int64, error) {
	var total int64
	for _, adapter := range r.adapters {
		if adapter.Kind() != KindHard {
			continue
		}
		count, err := adapter.Count(ctx, key)
		if err != nil {
			return 0, err
		}
		total += count
	}
	return total, nil
}

// List returns rows from one adapter.
func (r *Registry) List(ctx context.Context, adapterName, key string, limit, offset int) ([]Row, error) {
	for _, adapter := range r.adapters {
		if adapter.Name() == adapterName {
			return adapter.List(ctx, key, limit, offset)
		}
	}
	return nil, domain.ErrGroupNotFound
}
