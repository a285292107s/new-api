package model

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	// GroupNameAuto is a reserved pseudo group: it is not a real access group
	// but a token-level switch that lets the router probe an ordered list of
	// groups.
	GroupNameAuto = "auto"
	// GroupNameDefault is the built-in fallback group; tokens and users inherit
	// it when no group is configured.
	GroupNameDefault = "default"
	// MaxGroupNameLength mirrors the varchar(64) columns used across the
	// database schema (users.group, tokens.group, channels.group, ...).
	MaxGroupNameLength = 64
)

var (
	ErrGroupNotFound     = errors.New("group not found")
	ErrInvalidGroupName  = errors.New("invalid group name")
	ErrGroupNameExists   = errors.New("group name already exists")
	ErrBuiltinGroup      = errors.New("built-in group cannot be renamed")
	ErrGroupNameReserved = errors.New("group name is reserved")
)

// Group is the normalized group registry. Historically a group only existed as
// a bare string scattered across option JSON and varchar columns; the registry
// gives every group a stable primary key so names can be renamed atomically.
//
// Phase 1 keeps the legacy string projections authoritative for the query
// paths (users.group, tokens.group, channels.group, option JSON, ...) and
// mirrors the settings into this table. Rename updates the registry row and
// every projection in one database transaction.
type Group struct {
	Id          int     `json:"id" gorm:"primaryKey"`
	Name        string  `json:"name" gorm:"type:varchar(64);not null;uniqueIndex:idx_group_name"`
	Ratio       float64 `json:"ratio" gorm:"not null;default:1"`
	TopupRatio  float64 `json:"topup_ratio" gorm:"not null;default:1"`
	Description string  `json:"description" gorm:"type:varchar(255);not null;default:''"`
	AutoEnabled bool    `json:"auto_enabled" gorm:"not null;default:false"`
	Sort        int     `json:"sort" gorm:"not null;default:0"`
	CreatedAt   int64   `json:"created_at" gorm:"bigint"`
	UpdatedAt   int64   `json:"updated_at" gorm:"bigint"`
}

func (g *Group) BeforeCreate(tx *gorm.DB) error {
	now := common.GetTimestamp()
	if g.CreatedAt == 0 {
		g.CreatedAt = now
	}
	g.UpdatedAt = now
	return nil
}

func (g *Group) BeforeUpdate(tx *gorm.DB) error {
	g.UpdatedAt = common.GetTimestamp()
	return nil
}

// ValidateGroupName validates a group name before it is written anywhere. It
// rejects names that would corrupt the comma separated channel group column or
// the JSON projections on rename.
func ValidateGroupName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: name must not be empty", ErrInvalidGroupName)
	}
	if name != strings.TrimSpace(name) {
		return fmt.Errorf("%w: name must not start or end with whitespace", ErrInvalidGroupName)
	}
	if utf8.RuneCountInString(name) > MaxGroupNameLength {
		return fmt.Errorf("%w: name must be at most %d characters", ErrInvalidGroupName, MaxGroupNameLength)
	}
	if name == GroupNameAuto {
		return fmt.Errorf("%w: %q is a reserved pseudo group", ErrGroupNameReserved, name)
	}
	if strings.ContainsAny(name, ",\"\r\n\t") {
		return fmt.Errorf("%w: name must not contain commas, double quotes, tabs or line breaks", ErrInvalidGroupName)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: name must not contain control characters", ErrInvalidGroupName)
		}
	}
	return nil
}

// NormalizeGroupName trims and validates a user supplied group name.
func NormalizeGroupName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if err := ValidateGroupName(trimmed); err != nil {
		return "", err
	}
	return trimmed, nil
}

// NormalizeAssignableGroupName validates a group name that users or tokens
// reference. The empty string (inherit the user group) and the reserved auto
// pseudo group pass through unchanged.
func NormalizeAssignableGroupName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" || trimmed == GroupNameAuto {
		return trimmed, nil
	}
	return NormalizeGroupName(trimmed)
}

// NormalizeChannelGroupNames trims and validates a comma separated channel
// group list. Empty entries are dropped and duplicates collapsed while the
// original order is preserved, so the persisted CSV stays canonical and the
// legacy LIKE based matching keeps working.
func NormalizeChannelGroupNames(raw string) (string, error) {
	seen := make(map[string]struct{})
	names := make([]string, 0)
	for _, part := range strings.Split(raw, ",") {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		if err := ValidateGroupName(name); err != nil {
			return "", err
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return strings.Join(names, ","), nil
}

func GetGroupById(id int) (*Group, error) {
	var group Group
	if err := DB.First(&group, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrGroupNotFound
		}
		return nil, err
	}
	return &group, nil
}

func GetGroupByName(name string) (*Group, error) {
	if name == "" {
		return nil, ErrGroupNotFound
	}
	var group Group
	if err := DB.Where(map[string]any{"name": name}).First(&group).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrGroupNotFound
		}
		return nil, err
	}
	return &group, nil
}

// GetGroupIdByName resolves a group name to its registry id, registering the
// group first when the legacy string projections reference a group that has not
// been seeded yet.
func GetGroupIdByName(name string) (int, error) {
	group, err := GetGroupByName(name)
	if err == nil {
		return group.Id, nil
	}
	if !errors.Is(err, ErrGroupNotFound) {
		return 0, err
	}
	group, err = EnsureGroupRegistered(name)
	if err != nil {
		return 0, err
	}
	return group.Id, nil
}

// ListGroups returns the registry ordered by the auto-probe order first and the
// database id afterwards.
func ListGroups() ([]Group, error) {
	var groups []Group
	if err := DB.Order("sort asc, id asc").Find(&groups).Error; err != nil {
		return nil, err
	}
	return groups, nil
}

// EnsureGroupRegistered makes sure a single group name exists in the registry,
// seeding it from the current settings when necessary.
func EnsureGroupRegistered(name string) (*Group, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("%w: name must not be empty", ErrInvalidGroupName)
	}
	group, err := GetGroupByName(name)
	if err == nil {
		return group, nil
	}
	if !errors.Is(err, ErrGroupNotFound) {
		return nil, err
	}
	candidate := groupFromSettings(name)
	if err := DB.Clauses(clause.OnConflict{DoNothing: true}).Create(candidate).Error; err != nil {
		return nil, err
	}
	return GetGroupByName(name)
}

// EnsureGroupsInitialized seeds the registry from every place a group name is
// still stored as a bare string, then mirrors the settings into the registry
// rows. It is idempotent and safe to call on every startup.
func EnsureGroupsInitialized() error {
	names, err := CollectReferencedGroupNames()
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := ensureGroupRow(name); err != nil {
			return err
		}
	}
	return syncGroupsWithSettings()
}

func ensureGroupRow(name string) error {
	name = strings.TrimSpace(name)
	if name == "" || name == GroupNameAuto {
		return nil
	}
	if err := ValidateGroupName(name); err != nil {
		common.SysLog(common.LogText("skip registering invalid group name %q: %s", name, err.Error()))
		return nil
	}
	if _, err := GetGroupByName(name); err == nil {
		return nil
	} else if !errors.Is(err, ErrGroupNotFound) {
		return err
	}
	return DB.Clauses(clause.OnConflict{DoNothing: true}).Create(groupFromSettings(name)).Error
}

func groupFromSettings(name string) *Group {
	return &Group{
		Name:        name,
		Ratio:       groupRatioFromSettings(name),
		TopupRatio:  groupTopupRatioFromSettings(name),
		Description: setting.GetUserUsableGroupsCopy()[name],
		AutoEnabled: groupAutoEnabled(name),
		Sort:        groupAutoSort(name),
	}
}

func groupRatioFromSettings(name string) float64 {
	if ratio, ok := ratio_setting.GetGroupRatioCopy()[name]; ok {
		return ratio
	}
	return 1
}

func groupTopupRatioFromSettings(name string) float64 {
	if ratio, ok := common.GetTopupGroupRatioCopy()[name]; ok {
		return ratio
	}
	return 1
}

func groupAutoEnabled(name string) bool {
	for _, autoGroup := range setting.GetAutoGroups() {
		if autoGroup == name {
			return true
		}
	}
	return false
}

func groupAutoSort(name string) int {
	autoGroups := setting.GetAutoGroups()
	for i, autoGroup := range autoGroups {
		if autoGroup == name {
			return i
		}
	}
	return len(autoGroups)
}

func syncGroupsWithSettings() error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var groups []Group
		if err := tx.Find(&groups).Error; err != nil {
			return err
		}
		for i := range groups {
			group := &groups[i]
			ratio := groupRatioFromSettings(group.Name)
			topupRatio := groupTopupRatioFromSettings(group.Name)
			description := setting.GetUserUsableGroupsCopy()[group.Name]
			autoEnabled := groupAutoEnabled(group.Name)
			sortIndex := groupAutoSort(group.Name)
			if group.Ratio == ratio && group.TopupRatio == topupRatio && group.Description == description &&
				group.AutoEnabled == autoEnabled && group.Sort == sortIndex {
				continue
			}
			if err := tx.Model(&Group{}).Where(map[string]any{"id": group.Id}).Updates(map[string]any{
				"ratio":        ratio,
				"topup_ratio":  topupRatio,
				"description":  description,
				"auto_enabled": autoEnabled,
				"sort":         sortIndex,
				"updated_at":   common.GetTimestamp(),
			}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// CollectReferencedGroupNames gathers every group name currently referenced by
// settings or by persisted rows. The result is sorted for deterministic
// seeding.
func CollectReferencedGroupNames() ([]string, error) {
	names := make(map[string]struct{})
	for name := range ratio_setting.GetGroupRatioCopy() {
		names[name] = struct{}{}
	}
	for name := range setting.GetUserUsableGroupsCopy() {
		names[name] = struct{}{}
	}
	for _, name := range setting.GetAutoGroups() {
		names[name] = struct{}{}
	}
	for name := range common.GetTopupGroupRatioCopy() {
		names[name] = struct{}{}
	}
	for userGroup, inner := range ratio_setting.GetGroupGroupRatioCopy() {
		names[userGroup] = struct{}{}
		for usingGroup := range inner {
			names[usingGroup] = struct{}{}
		}
	}
	for userGroup, overrides := range ratio_setting.GetGroupSpecialUsableGroupCopy() {
		names[userGroup] = struct{}{}
		for entry := range overrides {
			names[stripGroupNamePrefix(entry)] = struct{}{}
		}
	}
	for name := range setting.GetModelRequestRateLimitGroupCopy() {
		names[name] = struct{}{}
	}

	dbNames, err := collectPersistedGroupNames(DB)
	if err != nil {
		return nil, err
	}
	for _, name := range dbNames {
		names[name] = struct{}{}
	}

	result := make([]string, 0, len(names))
	for name := range names {
		name = strings.TrimSpace(name)
		if name == "" || name == GroupNameAuto {
			continue
		}
		result = append(result, name)
	}
	sort.Strings(result)
	return result, nil
}

func quoteGroupColumn(db *gorm.DB, column string) string {
	switch db.Dialector.Name() {
	case "mysql":
		return "`" + column + "`"
	default:
		return `"` + column + `"`
	}
}

func collectPersistedGroupNames(db *gorm.DB) ([]string, error) {
	names := make([]string, 0)
	appendColumn := func(model any, column string) error {
		if !db.Migrator().HasTable(model) {
			return nil
		}
		var values []string
		quoted := quoteGroupColumn(db, column)
		if err := db.Model(model).Where(quoted+" IS NOT NULL AND "+quoted+" <> ''").Distinct().Pluck(column, &values).Error; err != nil {
			return err
		}
		names = append(names, values...)
		return nil
	}

	if err := appendColumn(&User{}, "group"); err != nil {
		return nil, err
	}
	if err := appendColumn(&Token{}, "group"); err != nil {
		return nil, err
	}
	if err := appendColumn(&Ability{}, "group"); err != nil {
		return nil, err
	}
	if err := appendColumn(&Task{}, "group"); err != nil {
		return nil, err
	}
	if err := appendColumn(&PerfMetric{}, "group"); err != nil {
		return nil, err
	}
	if err := appendColumn(&QuotaData{}, "use_group"); err != nil {
		return nil, err
	}
	if err := appendColumn(&SubscriptionPlan{}, "upgrade_group"); err != nil {
		return nil, err
	}
	if err := appendColumn(&SubscriptionPlan{}, "downgrade_group"); err != nil {
		return nil, err
	}
	if err := appendColumn(&UserSubscription{}, "upgrade_group"); err != nil {
		return nil, err
	}
	if err := appendColumn(&UserSubscription{}, "prev_user_group"); err != nil {
		return nil, err
	}
	if err := appendColumn(&UserSubscription{}, "downgrade_group"); err != nil {
		return nil, err
	}

	if db.Migrator().HasTable(&Channel{}) {
		var rawGroups []string
		if err := db.Model(&Channel{}).Pluck("group", &rawGroups).Error; err != nil {
			return nil, err
		}
		for _, raw := range rawGroups {
			for _, name := range splitChannelGroupNames(raw) {
				names = append(names, name)
			}
		}
	}

	if db.Migrator().HasTable(&Log{}) {
		var logGroups []string
		if err := db.Model(&Log{}).Distinct().Pluck("group", &logGroups).Error; err == nil {
			names = append(names, logGroups...)
		}
	}
	return names, nil
}

// CountGroupReferences reports how many persisted rows still reference a group
// name. It is used by the admin console to explain the blast radius of a
// rename and to warn about dangling references.
func CountGroupReferences(name string) (map[string]int64, error) {
	counts := make(map[string]int64)
	count := func(label string, model any, column string) error {
		if !DB.Migrator().HasTable(model) {
			return nil
		}
		var total int64
		if err := DB.Model(model).Where(map[string]any{column: name}).Count(&total).Error; err != nil {
			return err
		}
		counts[label] = total
		return nil
	}

	if err := count("users", &User{}, "group"); err != nil {
		return nil, err
	}
	if err := count("tokens", &Token{}, "group"); err != nil {
		return nil, err
	}
	if err := count("abilities", &Ability{}, "group"); err != nil {
		return nil, err
	}
	if err := count("tasks", &Task{}, "group"); err != nil {
		return nil, err
	}
	if err := count("perf_metrics", &PerfMetric{}, "group"); err != nil {
		return nil, err
	}
	if err := count("quota_data", &QuotaData{}, "use_group"); err != nil {
		return nil, err
	}
	if err := count("subscription_plans_upgrade", &SubscriptionPlan{}, "upgrade_group"); err != nil {
		return nil, err
	}
	if err := count("subscription_plans_downgrade", &SubscriptionPlan{}, "downgrade_group"); err != nil {
		return nil, err
	}

	if DB.Migrator().HasTable(&Channel{}) {
		var rawGroups []string
		if err := DB.Model(&Channel{}).Pluck("group", &rawGroups).Error; err != nil {
			return nil, err
		}
		var channelCount int64
		for _, raw := range rawGroups {
			for _, groupName := range splitChannelGroupNames(raw) {
				if groupName == name {
					channelCount++
					break
				}
			}
		}
		counts["channels"] = channelCount
	}
	return counts, nil
}

// RenameGroup renames a group and rewrites every legacy projection in a single
// transaction. Renaming the built-in default group is rejected because tokens
// and users fall back to it implicitly.
func RenameGroup(groupId int, newName string) (string, error) {
	normalized, err := NormalizeGroupName(newName)
	if err != nil {
		return "", err
	}
	group, err := GetGroupById(groupId)
	if err != nil {
		return "", err
	}
	oldName := group.Name
	if oldName == normalized {
		return oldName, nil
	}
	if oldName == GroupNameDefault {
		return "", fmt.Errorf("%w: %q", ErrBuiltinGroup, oldName)
	}

	optionValues, err := renamedGroupOptionValues(oldName, normalized)
	if err != nil {
		return "", err
	}

	var affectedUserIds []int
	var affectedTokenKeys []string
	err = DB.Transaction(func(tx *gorm.DB) error {
		var conflict int64
		if err := tx.Model(&Group{}).Where("name = ? and id <> ?", normalized, groupId).Count(&conflict).Error; err != nil {
			return err
		}
		if conflict > 0 {
			return fmt.Errorf("%w: %q", ErrGroupNameExists, normalized)
		}
		if err := tx.Model(&Group{}).Where(map[string]any{"id": groupId}).Updates(map[string]any{
			"name":       normalized,
			"updated_at": common.GetTimestamp(),
		}).Error; err != nil {
			return err
		}

		affectedUserIds, affectedTokenKeys, err = renameGroupReferences(tx, oldName, normalized)
		if err != nil {
			return err
		}
		return persistGroupOptionValues(tx, optionValues)
	})
	if err != nil {
		return "", err
	}

	for key, value := range optionValues {
		if err := updateOptionMap(key, value); err != nil {
			common.SysError(common.LogText("failed to apply renamed group option %s: %s", key, err.Error()))
		}
	}

	InitChannelCache()
	InvalidatePricingCache()
	for _, userId := range affectedUserIds {
		if err := RefreshUserGroupCache(userId); err != nil {
			common.SysError(common.LogText("failed to refresh user group cache for user %d: %s", userId, err.Error()))
		}
	}
	for _, tokenKey := range affectedTokenKeys {
		invalidateTokenCacheForMutation(tokenKey)
	}

	common.SysLog(common.LogText("group renamed: %s -> %s", oldName, normalized))
	return oldName, nil
}

func renameGroupReferences(tx *gorm.DB, oldName, newName string) ([]int, []string, error) {
	affectedUserIds := make([]int, 0)
	affectedTokenKeys := make([]string, 0)

	if tx.Migrator().HasTable(&User{}) {
		var userIds []int
		if err := tx.Model(&User{}).Where(map[string]any{"group": oldName}).Pluck("id", &userIds).Error; err != nil {
			return nil, nil, err
		}
		affectedUserIds = userIds
		if err := tx.Model(&User{}).Where(map[string]any{"group": oldName}).Update("group", newName).Error; err != nil {
			return nil, nil, err
		}
	}

	if tx.Migrator().HasTable(&Token{}) {
		var tokens []Token
		if err := tx.Select("id", "key", "group", "auto_groups").Where(map[string]any{"group": oldName}).Find(&tokens).Error; err != nil {
			return nil, nil, err
		}
		for _, token := range tokens {
			if token.Key != "" {
				affectedTokenKeys = append(affectedTokenKeys, token.Key)
			}
		}
		if err := tx.Model(&Token{}).Where(map[string]any{"group": oldName}).Update("group", newName).Error; err != nil {
			return nil, nil, err
		}
		if err := renameTokenAutoGroups(tx, oldName, newName, &affectedTokenKeys); err != nil {
			return nil, nil, err
		}
	}

	if tx.Migrator().HasTable(&Ability{}) {
		if err := tx.Model(&Ability{}).Where(map[string]any{"group": oldName}).Update("group", newName).Error; err != nil {
			return nil, nil, err
		}
	}
	if tx.Migrator().HasTable(&Task{}) {
		if err := tx.Model(&Task{}).Where(map[string]any{"group": oldName}).Update("group", newName).Error; err != nil {
			return nil, nil, err
		}
	}
	if tx.Migrator().HasTable(&PerfMetric{}) {
		if err := tx.Model(&PerfMetric{}).Where(map[string]any{"group": oldName}).Update("group", newName).Error; err != nil {
			return nil, nil, err
		}
	}
	if tx.Migrator().HasTable(&QuotaData{}) {
		if err := tx.Model(&QuotaData{}).Where(map[string]any{"use_group": oldName}).Update("use_group", newName).Error; err != nil {
			return nil, nil, err
		}
	}
	if tx.Migrator().HasTable(&SubscriptionPlan{}) {
		if err := tx.Model(&SubscriptionPlan{}).Where(map[string]any{"upgrade_group": oldName}).Update("upgrade_group", newName).Error; err != nil {
			return nil, nil, err
		}
		if err := tx.Model(&SubscriptionPlan{}).Where(map[string]any{"downgrade_group": oldName}).Update("downgrade_group", newName).Error; err != nil {
			return nil, nil, err
		}
	}
	if tx.Migrator().HasTable(&UserSubscription{}) {
		for _, column := range []string{"upgrade_group", "prev_user_group", "downgrade_group"} {
			if err := tx.Model(&UserSubscription{}).Where(map[string]any{column: oldName}).Update(column, newName).Error; err != nil {
				return nil, nil, err
			}
		}
	}
	if tx.Migrator().HasTable(&Channel{}) {
		if err := renameChannelGroups(tx, oldName, newName); err != nil {
			return nil, nil, err
		}
	}
	if err := renameLogGroups(tx, oldName, newName); err != nil {
		common.SysError(common.LogText("failed to rename group in log database: %s", err.Error()))
	}

	return affectedUserIds, affectedTokenKeys, nil
}

func renameTokenAutoGroups(tx *gorm.DB, oldName, newName string, affectedTokenKeys *[]string) error {
	var tokens []Token
	if err := tx.Select("id", "key", "auto_groups").Where("auto_groups is not null and auto_groups <> ''").Find(&tokens).Error; err != nil {
		return err
	}
	for _, token := range tokens {
		groups, err := token.GetAutoGroups()
		if err != nil {
			common.SysError(common.LogText("skip renaming auto groups of token %d: %s", token.Id, err.Error()))
			continue
		}
		changed := false
		for i, name := range groups {
			if strings.TrimSpace(name) == oldName {
				groups[i] = newName
				changed = true
			}
		}
		if !changed {
			continue
		}
		jsonValue, err := common.Marshal(groups)
		if err != nil {
			return err
		}
		if err := tx.Model(&Token{}).Where(map[string]any{"id": token.Id}).Update("auto_groups", string(jsonValue)).Error; err != nil {
			return err
		}
		if token.Key != "" {
			*affectedTokenKeys = append(*affectedTokenKeys, token.Key)
		}
	}
	return nil
}

func renameChannelGroups(tx *gorm.DB, oldName, newName string) error {
	var channels []Channel
	if err := tx.Select("id", "group").Find(&channels).Error; err != nil {
		return err
	}
	for _, channel := range channels {
		rewritten, changed := rewriteCommaSeparatedGroupNames(channel.Group, oldName, newName)
		if !changed {
			continue
		}
		if err := tx.Model(&Channel{}).Where(map[string]any{"id": channel.Id}).Update("group", rewritten).Error; err != nil {
			return err
		}
	}
	return nil
}

func renameLogGroups(tx *gorm.DB, oldName, newName string) error {
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) || LOG_DB == nil {
		return nil
	}
	logDB := LOG_DB
	// The log table usually shares the main database handle. Reuse the ongoing
	// transaction then, otherwise a second connection would deadlock behind the
	// transaction on single-connection setups.
	if logDB == DB {
		logDB = tx
	}
	if !logDB.Migrator().HasTable(&Log{}) {
		return nil
	}
	return logDB.Model(&Log{}).Where(map[string]any{"group": oldName}).Update("group", newName).Error
}

func persistGroupOptionValues(tx *gorm.DB, values map[string]string) error {
	for key, value := range values {
		option := Option{Key: key, Value: value}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "key"}},
			DoUpdates: clause.AssignmentColumns([]string{"value"}),
		}).Create(&option).Error; err != nil {
			return err
		}
	}
	return nil
}

// renamedGroupOptionValues rewrites the configured group name inside every
// option JSON that stores groups as keys or values. Only values that actually
// change are returned, keyed by option key.
func renamedGroupOptionValues(oldName, newName string) (map[string]string, error) {
	updates := make(map[string]string)

	if updated, changed := renameMapKey(ratio_setting.GetGroupRatioCopy(), oldName, newName); changed {
		encoded, err := common.Marshal(updated)
		if err != nil {
			return nil, err
		}
		updates["GroupRatio"] = string(encoded)
	}

	groupGroupRatio := make(map[string]map[string]float64)
	groupGroupRatioChanged := false
	for userGroup, inner := range ratio_setting.GetGroupGroupRatioCopy() {
		newInner := make(map[string]float64, len(inner))
		innerChanged := false
		for usingGroup, ratio := range inner {
			if usingGroup == oldName {
				newInner[newName] = ratio
				innerChanged = true
				continue
			}
			newInner[usingGroup] = ratio
		}
		newUserGroup := userGroup
		if userGroup == oldName {
			newUserGroup = newName
			innerChanged = true
		}
		if innerChanged {
			groupGroupRatioChanged = true
		}
		groupGroupRatio[newUserGroup] = newInner
	}
	if groupGroupRatioChanged {
		encoded, err := common.Marshal(groupGroupRatio)
		if err != nil {
			return nil, err
		}
		updates["GroupGroupRatio"] = string(encoded)
	}

	if updated, changed := renameMapKey(setting.GetUserUsableGroupsCopy(), oldName, newName); changed {
		encoded, err := common.Marshal(updated)
		if err != nil {
			return nil, err
		}
		updates["UserUsableGroups"] = string(encoded)
	}

	if updated, changed := renameMapKey(common.GetTopupGroupRatioCopy(), oldName, newName); changed {
		encoded, err := common.Marshal(updated)
		if err != nil {
			return nil, err
		}
		updates["TopupGroupRatio"] = string(encoded)
	}

	// GetAutoGroups returns the live slice: copy it before mutating so a failed
	// rename cannot leak the new name into memory.
	liveAutoGroups := setting.GetAutoGroups()
	autoGroups := make([]string, len(liveAutoGroups))
	copy(autoGroups, liveAutoGroups)
	autoGroupsChanged := false
	for i, name := range autoGroups {
		if name == oldName {
			autoGroups[i] = newName
			autoGroupsChanged = true
		}
	}
	if autoGroupsChanged {
		encoded, err := common.Marshal(autoGroups)
		if err != nil {
			return nil, err
		}
		updates["AutoGroups"] = string(encoded)
	}

	specialUsableGroups := make(map[string]map[string]string)
	specialUsableChanged := false
	for userGroup, overrides := range ratio_setting.GetGroupSpecialUsableGroupCopy() {
		newOverrides := make(map[string]string, len(overrides))
		overridesChanged := false
		for entry, description := range overrides {
			prefix := groupNamePrefix(entry)
			if stripGroupNamePrefix(entry) == oldName {
				newOverrides[prefix+newName] = description
				overridesChanged = true
				continue
			}
			newOverrides[entry] = description
		}
		newUserGroup := userGroup
		if userGroup == oldName {
			newUserGroup = newName
			overridesChanged = true
		}
		if overridesChanged {
			specialUsableChanged = true
		}
		specialUsableGroups[newUserGroup] = newOverrides
	}
	if specialUsableChanged {
		encoded, err := common.Marshal(specialUsableGroups)
		if err != nil {
			return nil, err
		}
		updates["group_ratio_setting.group_special_usable_group"] = string(encoded)
	}

	if updated, changed := renameMapKey(setting.GetModelRequestRateLimitGroupCopy(), oldName, newName); changed {
		encoded, err := common.Marshal(updated)
		if err != nil {
			return nil, err
		}
		updates["ModelRequestRateLimitGroup"] = string(encoded)
	}

	return updates, nil
}

func renameMapKey[V any](source map[string]V, oldKey, newKey string) (map[string]V, bool) {
	value, ok := source[oldKey]
	if !ok {
		return source, false
	}
	updated := make(map[string]V, len(source))
	for key, item := range source {
		if key == oldKey {
			continue
		}
		updated[key] = item
	}
	updated[newKey] = value
	return updated, true
}

func groupNamePrefix(entry string) string {
	switch {
	case strings.HasPrefix(entry, "-:"):
		return "-:"
	case strings.HasPrefix(entry, "+:"):
		return "+:"
	default:
		return ""
	}
}

func stripGroupNamePrefix(entry string) string {
	if prefix := groupNamePrefix(entry); prefix != "" {
		return strings.TrimPrefix(entry, prefix)
	}
	return entry
}

func splitChannelGroupNames(raw string) []string {
	groups := make([]string, 0)
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		groups = append(groups, part)
	}
	return groups
}

func rewriteCommaSeparatedGroupNames(raw, oldName, newName string) (string, bool) {
	parts := strings.Split(raw, ",")
	changed := false
	for i, part := range parts {
		if strings.TrimSpace(part) == oldName {
			parts[i] = newName
			changed = true
		}
	}
	if !changed {
		return raw, false
	}
	return strings.Join(parts, ","), true
}
