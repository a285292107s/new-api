package model

import (
	"context"
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/group/domain"
	"github.com/QuantumNous/new-api/pkg/group/registry"
	"gorm.io/gorm"
)

// groupLegacySync writes the registry back into the legacy `groups` table and
// refreshes the in-memory option map.
//
// Two invariants hold here:
//  1. The legacy `groups` table is a compatibility copy for an older image to
//     read after a rollback; it must track every registry write.
//  2. The in-process option map is what the not-yet-migrated read paths consult,
//     so it must be refreshed immediately rather than waiting for the 60s
//     SyncOptions tick. Otherwise a v2 ratio change can be pre-consumed at one
//     price and settled at another.

// syncLegacyGroupsTable mirrors one registry row into `groups`.
func syncLegacyGroupsTable(ctx context.Context, tx *gorm.DB, group domain.Group) error {
	if tx == nil {
		tx = DB
	}
	var legacy Group
	err := tx.Where("name = ?", group.Key).First(&legacy).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		legacy = Group{
			Id:          int(group.ID),
			Name:        group.Key,
			Ratio:       group.Ratio,
			TopupRatio:  group.TopupRatio,
			Description: group.Description,
			AutoEnabled: group.AutoSelectable,
			Sort:        group.Sort,
		}
		return tx.Create(&legacy).Error
	case err != nil:
		return err
	default:
		return tx.Model(&Group{}).Where("id = ?", legacy.Id).Updates(map[string]any{
			"ratio":        group.Ratio,
			"topup_ratio":  group.TopupRatio,
			"description":  group.Description,
			"auto_enabled": group.AutoSelectable,
			"sort":         group.Sort,
		}).Error
	}
}

// applyOptionsToMemory pushes the freshly projected option values into the
// process-wide OptionMap so legacy readers see them on the very next request.
func applyOptionsToMemory(values map[string]string) {
	if len(values) == 0 {
		return
	}
	common.OptionMapRWMutex.Lock()
	for key, value := range values {
		common.OptionMap[key] = value
	}
	common.OptionMapRWMutex.Unlock()
	for key, value := range values {
		if err := updateOptionMap(key, value); err != nil {
			common.SysError(common.LogText("failed to apply group option %s to memory: %s", key, err.Error()))
		}
	}
}

// RefreshLegacyProjections is the single "make old code see the new truth"
// entry point. Call it after any registry write.
func RefreshLegacyProjections(ctx context.Context, service *GroupService) error {
	if service == nil {
		return nil
	}
	groups, err := service.Store.List(ctx, true)
	if err != nil {
		return err
	}
	err = DB.Transaction(func(tx *gorm.DB) error {
		for _, group := range groups {
			if err := syncLegacyGroupsTable(ctx, tx, group); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("sync legacy groups table: %w", err)
	}
	projected, err := service.projectOptions(ctx)
	if err != nil {
		return err
	}
	applyOptionsToMemory(projected)
	return nil
}

var _ = registry.AllModels
