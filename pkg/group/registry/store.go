package registry

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/group/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Repository is the persistence port the lifecycle/snapshot/assignment layers
// depend on. Keeping it an interface lets the rest of pkg/group stay free of
// GORM and makes the module unit-testable with an in-memory fake.
type Repository interface {
	List(ctx context.Context, includeArchived bool) ([]domain.Group, error)
	Get(ctx context.Context, key string) (*domain.Group, error)
	Create(ctx context.Context, group domain.Group) (*domain.Group, error)
	Update(ctx context.Context, group domain.Group, expectedVersion int64) (*domain.Group, error)
	ChangeStatus(ctx context.Context, key string, to domain.Status, expectedVersion int64) (*domain.Group, error)
	Delete(ctx context.Context, key string) error // physical, only used by tests/migrations

	ListGroupGroupRatios(ctx context.Context) ([]domain.GroupGroupRatio, error)
	ListVisibilityRules(ctx context.Context) ([]domain.VisibilityRule, error)
	ListRateLimits(ctx context.Context) ([]domain.RateLimit, error)
	ReplaceGroupGroupRatios(ctx context.Context, rows []domain.GroupGroupRatio) error
	ReplaceVisibilityRules(ctx context.Context, rows []domain.VisibilityRule) error
	ReplaceRateLimits(ctx context.Context, rows []domain.RateLimit) error

	BumpVersion(ctx context.Context, tx *gorm.DB) (int64, error)
	CurrentVersion(ctx context.Context) (int64, error)
}

// Store implements Repository on top of GORM.
type Store struct {
	db *gorm.DB
}

// NewStore wires a Store to the caller-provided database handle. The handle is
// injected rather than read from a package global so pkg/group never imports
// model (which would create a cycle once model adapts to this package).
func NewStore(db *gorm.DB) *Store {
	return &Store{db: db}
}

// DB exposes the handle for callers that need to run a shared transaction with
// legacy tables (lifecycle keeps that inside the model adapter instead).
func (s *Store) DB() *gorm.DB { return s.db }

func (s *Store) List(ctx context.Context, includeArchived bool) ([]domain.Group, error) {
	query := s.db.WithContext(ctx).Order("sort asc, id asc")
	if !includeArchived {
		query = query.Where("status <> ?", string(domain.StatusArchived))
	}
	var rows []GroupRegistry
	if err := query.Find(&rows).Error; err != nil {
		return nil, err
	}
	groups := make([]domain.Group, 0, len(rows))
	for i := range rows {
		groups = append(groups, rows[i].toDomain())
	}
	return groups, nil
}

func (s *Store) Get(ctx context.Context, key string) (*domain.Group, error) {
	var row GroupRegistry
	err := s.db.WithContext(ctx).Where("group_key = ?", key).First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrGroupNotFound
		}
		return nil, err
	}
	group := row.toDomain()
	return &group, nil
}

func (s *Store) Create(ctx context.Context, group domain.Group) (*domain.Group, error) {
	if err := group.Validate(); err != nil {
		return nil, err
	}
	row := GroupRegistry{
		GroupKey:       group.Key,
		DisplayName:    group.DisplayName,
		DisplayNorm:    domain.NormalizeDisplayNameForLookup(group.DisplayName),
		Status:         string(group.Status),
		IsDefault:      group.IsDefault,
		Ratio:          group.Ratio,
		TopupRatio:     group.TopupRatio,
		Description:    group.Description,
		Sort:           group.Sort,
		AutoSelectable: group.AutoSelectable,
		AutoSort:       group.AutoSort,
		UserSelectable: group.UserSelectable,
		Version:        max(group.Version, 1),
		CreatedAt:      group.CreatedAt,
		UpdatedAt:      group.UpdatedAt,
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return nil, classifyWriteError(err)
	}
	created := row.toDomain()
	return &created, nil
}

func (s *Store) Update(ctx context.Context, group domain.Group, expectedVersion int64) (*domain.Group, error) {
	if err := group.Validate(); err != nil {
		return nil, err
	}
	now := common.GetTimestamp()
	result := s.db.WithContext(ctx).Model(&GroupRegistry{}).
		Where("group_key = ? AND version = ?", group.Key, expectedVersion).
		Updates(map[string]any{
			"display_name":    group.DisplayName,
			"display_norm":    domain.NormalizeDisplayNameForLookup(group.DisplayName),
			"ratio":           group.Ratio,
			"topup_ratio":     group.TopupRatio,
			"description":     group.Description,
			"sort":            group.Sort,
			"auto_selectable": group.AutoSelectable,
			"auto_sort":       group.AutoSort,
			"user_selectable": group.UserSelectable,
			"version":         expectedVersion + 1,
			"updated_at":      now,
		})
	if result.Error != nil {
		return nil, classifyWriteError(result.Error)
	}
	if result.RowsAffected == 0 {
		// Distinguish "gone" from "version raced" for a useful error.
		if _, err := s.Get(ctx, group.Key); err != nil {
			return nil, err
		}
		return nil, domain.ErrVersionConflict
	}
	return s.Get(ctx, group.Key)
}

// ChangeStatus flips the lifecycle state with a version CAS.
func (s *Store) ChangeStatus(ctx context.Context, key string, to domain.Status, expectedVersion int64) (*domain.Group, error) {
	updates := map[string]any{
		"status":     string(to),
		"version":    expectedVersion + 1,
		"updated_at": common.GetTimestamp(),
	}
	// Leaving the active state stops auto probing, but re-enabling a group must
	// not silently forget that it had been selectable — only an explicit
	// lifecycle write clears that flag.
	if to != domain.StatusActive {
		updates["auto_selectable"] = false
	}
	result := s.db.WithContext(ctx).Model(&GroupRegistry{}).
		Where("group_key = ? AND version = ?", key, expectedVersion).
		Updates(updates)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		if _, err := s.Get(ctx, key); err != nil {
			return nil, err
		}
		return nil, domain.ErrVersionConflict
	}
	return s.Get(ctx, key)
}

func (s *Store) Delete(ctx context.Context, key string) error {
	return s.db.WithContext(ctx).Where("group_key = ?", key).Delete(&GroupRegistry{}).Error
}

func (s *Store) ListGroupGroupRatios(ctx context.Context) ([]domain.GroupGroupRatio, error) {
	var rows []GroupGroupRatio
	if err := s.db.WithContext(ctx).Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]domain.GroupGroupRatio, 0, len(rows))
	for i := range rows {
		result = append(result, domain.GroupGroupRatio{
			UserGroupKey:  rows[i].UserGroupKey,
			UsingGroupKey: rows[i].UsingGroupKey,
			Ratio:         rows[i].Ratio,
		})
	}
	return result, nil
}

func (s *Store) ListVisibilityRules(ctx context.Context) ([]domain.VisibilityRule, error) {
	var rows []GroupVisibilityRule
	if err := s.db.WithContext(ctx).Order("id asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]domain.VisibilityRule, 0, len(rows))
	for i := range rows {
		result = append(result, domain.VisibilityRule{
			UserGroupKey:   rows[i].UserGroupKey,
			Op:             domain.VisibilityOp(rows[i].Op),
			TargetGroupKey: rows[i].TargetGroupKey,
			Description:    rows[i].Description,
		})
	}
	return result, nil
}

func (s *Store) ListRateLimits(ctx context.Context) ([]domain.RateLimit, error) {
	var rows []GroupRateLimit
	if err := s.db.WithContext(ctx).Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]domain.RateLimit, 0, len(rows))
	for i := range rows {
		result = append(result, domain.RateLimit{
			GroupKey:     rows[i].GroupKey,
			TotalCount:   rows[i].TotalCount,
			SuccessCount: rows[i].SuccessCount,
		})
	}
	return result, nil
}

func (s *Store) ReplaceGroupGroupRatios(ctx context.Context, rows []domain.GroupGroupRatio) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("1 = 1").Delete(&GroupGroupRatio{}).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		models := make([]GroupGroupRatio, 0, len(rows))
		for _, row := range rows {
			models = append(models, GroupGroupRatio{
				UserGroupKey:  row.UserGroupKey,
				UsingGroupKey: row.UsingGroupKey,
				Ratio:         row.Ratio,
			})
		}
		return tx.Create(&models).Error
	})
}

func (s *Store) ReplaceVisibilityRules(ctx context.Context, rows []domain.VisibilityRule) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("1 = 1").Delete(&GroupVisibilityRule{}).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		models := make([]GroupVisibilityRule, 0, len(rows))
		for _, row := range rows {
			models = append(models, GroupVisibilityRule{
				UserGroupKey:   row.UserGroupKey,
				Op:             string(row.Op),
				TargetGroupKey: row.TargetGroupKey,
				Description:    row.Description,
			})
		}
		return tx.Create(&models).Error
	})
}

func (s *Store) ReplaceRateLimits(ctx context.Context, rows []domain.RateLimit) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("1 = 1").Delete(&GroupRateLimit{}).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		models := make([]GroupRateLimit, 0, len(rows))
		for _, row := range rows {
			models = append(models, GroupRateLimit{
				GroupKey:     row.GroupKey,
				TotalCount:   row.TotalCount,
				SuccessCount: row.SuccessCount,
			})
		}
		return tx.Create(&models).Error
	})
}

// BumpVersion increments the single-row meta version and returns the new value.
// Callers pass their transaction so the version bump commits atomically with
// the row change; nil means "run standalone".
func (s *Store) BumpVersion(ctx context.Context, tx *gorm.DB) (int64, error) {
	if tx == nil {
		tx = s.db
	}
	var meta GroupMeta
	err := tx.WithContext(ctx).Where("id = 1").First(&meta).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		meta = GroupMeta{Id: 1, Version: 1}
		if err := tx.WithContext(ctx).Create(&meta).Error; err != nil {
			return 0, err
		}
		return meta.Version, nil
	}
	if err != nil {
		return 0, err
	}
	next := meta.Version + 1
	if err := tx.WithContext(ctx).Model(&GroupMeta{}).Where("id = 1").Update("version", next).Error; err != nil {
		return 0, err
	}
	return next, nil
}

func (s *Store) CurrentVersion(ctx context.Context) (int64, error) {
	var meta GroupMeta
	err := s.db.WithContext(ctx).Where("id = 1").First(&meta).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return meta.Version, nil
}

// AppendChangeRecord writes an assignment record, or returns the existing one
// when the idempotency key was already used. The second return value reports
// whether this call created the row.
func (s *Store) AppendChangeRecord(ctx context.Context, record GroupChangeRecord) (*GroupChangeRecord, bool, error) {
	return s.AppendChangeRecordTx(s.db.WithContext(ctx), record)
}

// AppendChangeRecordTx is AppendChangeRecord inside a caller transaction.
func (s *Store) AppendChangeRecordTx(tx *gorm.DB, record GroupChangeRecord) (*GroupChangeRecord, bool, error) {
	record.CreatedAt = common.GetTimestamp()
	result := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "idempotency_key"}},
		DoNothing: true,
	}).Create(&record)
	if result.Error != nil {
		return nil, false, result.Error
	}
	if result.RowsAffected == 1 {
		return &record, true, nil
	}
	var existing GroupChangeRecord
	if err := tx.Where("idempotency_key = ?", record.IdempotencyKey).First(&existing).Error; err != nil {
		return nil, false, err
	}
	return &existing, false, nil
}

// GetID returns the numeric row id for a key, used by the v1 compatibility
// adapter (the frozen console addresses groups by id).
func (s *Store) GetID(ctx context.Context, key string) (int64, error) {
	var row GroupRegistry
	if err := s.db.WithContext(ctx).Select("id").Where("group_key = ?", key).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, domain.ErrGroupNotFound
		}
		return 0, err
	}
	return row.Id, nil
}

// BumpVersionTx is BumpVersion inside a caller transaction.
func (s *Store) BumpVersionTx(tx *gorm.DB) (int64, error) {
	return s.BumpVersion(context.Background(), tx)
}

// AppendEvent writes one outbox event.
func (s *Store) AppendEvent(ctx context.Context, eventType, payload string) error {
	return s.db.WithContext(ctx).Create(&GroupEvent{
		EventType: eventType,
		Payload:   payload,
		CreatedAt: common.GetTimestamp(),
	}).Error
}

func classifyWriteError(err error) error {
	if err == nil {
		return nil
	}
	// GORM returns driver-specific duplicate-key errors; the case-insensitive
	// substring check keeps this dependency-free across SQLite/MySQL/PostgreSQL.
	message := strings.ToLower(err.Error())
	for _, needle := range []string{
		"unique constraint failed",
		"duplicate entry",
		"duplicate key value violates unique constraint",
	} {
		if strings.Contains(message, needle) {
			return fmt.Errorf("%w: %s", domain.ErrDisplayNameExists, err.Error())
		}
	}
	return err
}
