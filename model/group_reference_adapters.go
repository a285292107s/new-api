package model

import (
	"context"
	"fmt"

	"github.com/QuantumNous/new-api/pkg/group/reference"
	"gorm.io/gorm"
)

// Each adapter owns one legacy table that stores a group key. Adding a new
// group-bearing table only needs one more registration in
// newGroupReferenceAdapters, never a change to archive or lifecycle logic.

type groupUserRefAdapter struct{ db *gorm.DB }

func (a groupUserRefAdapter) Name() string         { return "users" }
func (a groupUserRefAdapter) Kind() reference.Kind { return reference.KindHard }

func (a groupUserRefAdapter) Count(ctx context.Context, key string) (int64, error) {
	if !a.db.Migrator().HasTable(&User{}) {
		return 0, nil
	}
	var total int64
	err := a.db.WithContext(ctx).Model(&User{}).Where(map[string]any{"group": key}).Count(&total).Error
	return total, err
}

func (a groupUserRefAdapter) List(ctx context.Context, key string, limit, offset int) ([]reference.Row, error) {
	var rows []User
	err := a.db.WithContext(ctx).Select("id", "username", groupColumn()).Where(map[string]any{"group": key}).
		Order("id asc").Limit(limit).Offset(offset).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	result := make([]reference.Row, 0, len(rows))
	for _, row := range rows {
		result = append(result, reference.Row{Table: "users", Id: int64(row.Id), Label: row.Username})
	}
	return result, nil
}

type groupTokenRefAdapter struct{ db *gorm.DB }

func (a groupTokenRefAdapter) Name() string         { return "tokens" }
func (a groupTokenRefAdapter) Kind() reference.Kind { return reference.KindHard }

func (a groupTokenRefAdapter) Count(ctx context.Context, key string) (int64, error) {
	if !a.db.Migrator().HasTable(&Token{}) {
		return 0, nil
	}
	var total int64
	err := a.db.WithContext(ctx).Model(&Token{}).Where(map[string]any{"group": key}).Count(&total).Error
	return total, err
}

func (a groupTokenRefAdapter) List(ctx context.Context, key string, limit, offset int) ([]reference.Row, error) {
	var rows []Token
	err := a.db.WithContext(ctx).Select("id", "name", groupColumn()).Where(map[string]any{"group": key}).
		Order("id asc").Limit(limit).Offset(offset).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	result := make([]reference.Row, 0, len(rows))
	for _, row := range rows {
		result = append(result, reference.Row{Table: "tokens", Id: int64(row.Id), Label: row.Name})
	}
	return result, nil
}

// groupChannelRefAdapter reads the channels.group CSV. It loads the column and
// splits in Go because a CSV membership test is not portable SQL.
type groupChannelRefAdapter struct{ db *gorm.DB }

func (a groupChannelRefAdapter) Name() string         { return "channels" }
func (a groupChannelRefAdapter) Kind() reference.Kind { return reference.KindHard }

func (a groupChannelRefAdapter) Count(ctx context.Context, key string) (int64, error) {
	if !a.db.Migrator().HasTable(&Channel{}) {
		return 0, nil
	}
	var raw []string
	if err := a.db.WithContext(ctx).Model(&Channel{}).Pluck("group", &raw).Error; err != nil {
		return 0, err
	}
	var total int64
	for _, csv := range raw {
		for _, name := range splitChannelGroupNames(csv) {
			if name == key {
				total++
				break
			}
		}
	}
	return total, nil
}

func (a groupChannelRefAdapter) List(ctx context.Context, key string, limit, offset int) ([]reference.Row, error) {
	var rows []Channel
	if err := a.db.WithContext(ctx).Select("id", "name", "group").Order("id asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	matched := make([]reference.Row, 0)
	for _, row := range rows {
		for _, name := range splitChannelGroupNames(row.Group) {
			if name != key {
				continue
			}
			matched = append(matched, reference.Row{Table: "channels", Id: int64(row.Id), Label: row.Name})
			break
		}
	}
	return paginateReferenceRows(matched, limit, offset), nil
}

type groupAbilityRefAdapter struct{ db *gorm.DB }

func (a groupAbilityRefAdapter) Name() string         { return "abilities" }
func (a groupAbilityRefAdapter) Kind() reference.Kind { return reference.KindSoft }

func (a groupAbilityRefAdapter) Count(ctx context.Context, key string) (int64, error) {
	if !a.db.Migrator().HasTable(&Ability{}) {
		return 0, nil
	}
	var total int64
	err := a.db.WithContext(ctx).Model(&Ability{}).Where(map[string]any{"group": key}).Count(&total).Error
	return total, err
}

func (a groupAbilityRefAdapter) List(ctx context.Context, key string, limit, offset int) ([]reference.Row, error) {
	var rows []Ability
	err := a.db.WithContext(ctx).Select("group", "model", "channel_id").Where(map[string]any{"group": key}).
		Order("model asc").Limit(limit).Offset(offset).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	result := make([]reference.Row, 0, len(rows))
	for _, row := range rows {
		result = append(result, reference.Row{Table: "abilities", Id: int64(row.ChannelId), Label: fmt.Sprintf("%s/%s", row.Model, row.Group)})
	}
	return result, nil
}

type groupSubscriptionRefAdapter struct{ db *gorm.DB }

func (a groupSubscriptionRefAdapter) Name() string         { return "subscriptions" }
func (a groupSubscriptionRefAdapter) Kind() reference.Kind { return reference.KindHard }

func (a groupSubscriptionRefAdapter) Count(ctx context.Context, key string) (int64, error) {
	if !a.db.Migrator().HasTable(&SubscriptionPlan{}) {
		return 0, nil
	}
	var plans int64
	if err := a.db.WithContext(ctx).Model(&SubscriptionPlan{}).
		Where("upgrade_group = ? OR downgrade_group = ?", key, key).Count(&plans).Error; err != nil {
		return 0, err
	}
	var subs int64
	if err := a.db.WithContext(ctx).Model(&UserSubscription{}).
		Where("upgrade_group = ? OR prev_user_group = ? OR downgrade_group = ?", key, key, key).Count(&subs).Error; err != nil {
		return 0, err
	}
	return plans + subs, nil
}

func (a groupSubscriptionRefAdapter) List(ctx context.Context, key string, limit, offset int) ([]reference.Row, error) {
	var plans []SubscriptionPlan
	if err := a.db.WithContext(ctx).Select("id", "title", "upgrade_group", "downgrade_group").Find(&plans).Error; err != nil {
		return nil, err
	}
	matched := make([]reference.Row, 0)
	for _, plan := range plans {
		if plan.UpgradeGroup == key || plan.DowngradeGroup == key {
			matched = append(matched, reference.Row{Table: "subscription_plans", Id: int64(plan.Id), Label: plan.Title})
		}
	}
	return paginateReferenceRows(matched, limit, offset), nil
}

type groupLogRefAdapter struct{ db *gorm.DB }

func (a groupLogRefAdapter) Name() string         { return "logs" }
func (a groupLogRefAdapter) Kind() reference.Kind { return reference.KindSoft }

func (a groupLogRefAdapter) Count(ctx context.Context, key string) (int64, error) {
	if !a.db.Migrator().HasTable(&Log{}) {
		return 0, nil
	}
	var total int64
	err := a.db.WithContext(ctx).Model(&Log{}).Where(map[string]any{"group": key}).Count(&total).Error
	return total, err
}

func (a groupLogRefAdapter) List(ctx context.Context, key string, limit, offset int) ([]reference.Row, error) {
	var rows []Log
	err := a.db.WithContext(ctx).Select("id", "username", "model_name", "created_at").Where(map[string]any{"group": key}).
		Order("id desc").Limit(limit).Offset(offset).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	result := make([]reference.Row, 0, len(rows))
	for _, row := range rows {
		result = append(result, reference.Row{Table: "logs", Id: int64(row.Id), Label: fmt.Sprintf("%s/%s", row.Username, row.ModelName)})
	}
	return result, nil
}

type groupQuotaDataRefAdapter struct{ db *gorm.DB }

func (a groupQuotaDataRefAdapter) Name() string         { return "quota_data" }
func (a groupQuotaDataRefAdapter) Kind() reference.Kind { return reference.KindSoft }

func (a groupQuotaDataRefAdapter) Count(ctx context.Context, key string) (int64, error) {
	if !a.db.Migrator().HasTable(&QuotaData{}) {
		return 0, nil
	}
	var total int64
	err := a.db.WithContext(ctx).Model(&QuotaData{}).Where(map[string]any{"use_group": key}).Count(&total).Error
	return total, err
}

func (a groupQuotaDataRefAdapter) List(ctx context.Context, key string, limit, offset int) ([]reference.Row, error) {
	var rows []QuotaData
	err := a.db.WithContext(ctx).Select("id", "model_name", "created_at").Where(map[string]any{"use_group": key}).
		Order("id desc").Limit(limit).Offset(offset).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	result := make([]reference.Row, 0, len(rows))
	for _, row := range rows {
		result = append(result, reference.Row{Table: "quota_data", Id: int64(row.Id), Label: fmt.Sprintf("%s@%d", row.ModelName, row.CreatedAt)})
	}
	return result, nil
}

// newGroupReferenceAdapters builds the default hard+soft adapter set.
func newGroupReferenceAdapters(db *gorm.DB) *reference.Registry {
	return reference.NewRegistry(
		groupUserRefAdapter{db: db},
		groupTokenRefAdapter{db: db},
		groupChannelRefAdapter{db: db},
		groupSubscriptionRefAdapter{db: db},
		groupAbilityRefAdapter{db: db},
		groupLogRefAdapter{db: db},
		groupQuotaDataRefAdapter{db: db},
	)
}

func paginateReferenceRows(rows []reference.Row, limit, offset int) []reference.Row {
	if offset > len(rows) {
		return nil
	}
	rows = rows[offset:]
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows
}
