package controller

import (
	"context"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/group/domain"
	"github.com/QuantumNous/new-api/pkg/group/lifecycle"
	"github.com/QuantumNous/new-api/setting"

	"github.com/gin-gonic/gin"
)

// The functions in this file are the v1 compatibility layer. They keep the
// original JSON shapes so the frozen embedded console keeps working, while the
// data they read and write comes from the new authoritative registry.

// GroupRegistryItem is the v1 registry row shape.
type GroupRegistryItem struct {
	ID          int64            `json:"id"`
	Name        string           `json:"name"`
	Ratio       float64          `json:"ratio"`
	TopupRatio  float64          `json:"topup_ratio"`
	Description string           `json:"description"`
	AutoEnabled bool             `json:"auto_enabled"`
	Sort        int              `json:"sort"`
	Key         string           `json:"key,omitempty"`
	Status      string           `json:"status,omitempty"`
	Version     int64            `json:"version,omitempty"`
	References  map[string]int64 `json:"references"`
}

// GetGroups keeps the v1 `GET /api/group/` shape: a plain array of keys.
//
// It must be keys, not display names: the frozen console uses these strings as
// the value it writes into users.group / tokens.group / channels.group. After a
// display-name change a display name would no longer resolve to a group.
func GetGroups(c *gin.Context) {
	service := model.CurrentGroupService()
	if service != nil {
		groups, err := service.Store.List(c.Request.Context(), false)
		if err == nil {
			names := make([]string, 0, len(groups))
			for _, group := range groups {
				names = append(names, group.Key)
			}
			c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": names})
			return
		}
		common.SysError(common.LogText("v1 group list fell back to legacy path: %s", err.Error()))
	}
	legacyGetGroups(c)
}

// GetGroupRegistry keeps the v1 registry shape, backed by the new registry.
func GetGroupRegistry(c *gin.Context) {
	service := model.CurrentGroupService()
	if service == nil {
		legacyGetGroupRegistry(c)
		return
	}
	groups, err := service.Store.List(c.Request.Context(), true)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	items := make([]GroupRegistryItem, 0, len(groups))
	for _, group := range groups {
		references, err := service.References.Counts(c.Request.Context(), group.Key)
		if err != nil {
			common.ApiError(c, err)
			return
		}
		items = append(items, GroupRegistryItem{
			ID:          groupKeyToLegacyId(service, group.Key),
			Name:        group.DisplayName,
			Ratio:       group.Ratio,
			TopupRatio:  group.TopupRatio,
			Description: group.Description,
			AutoEnabled: group.AutoSelectable,
			Sort:        group.Sort,
			Key:         group.Key,
			Status:      string(group.Status),
			Version:     group.Version,
			References:  references,
		})
	}
	common.ApiSuccess(c, items)
}

// RenameGroup keeps the v1 rename endpoint, but it now only changes the display
// name: the immutable key is untouched, so no business table is rewritten.
func RenameGroup(c *gin.Context) {
	var request renameGroupRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		common.ApiError(c, err)
		return
	}
	if request.Id == 0 {
		common.ApiErrorT(c, "Missing group ID")
		return
	}
	service := model.CurrentGroupService()
	if service == nil {
		legacyRenameGroup(c, request)
		return
	}
	groups, err := service.Store.List(c.Request.Context(), true)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	var target *domain.Group
	for i := range groups {
		if groupKeyToLegacyId(service, groups[i].Key) == int64(request.Id) {
			target = &groups[i]
			break
		}
	}
	if target == nil {
		common.ApiErrorT(c, "Group not found")
		return
	}
	name := strings.TrimSpace(request.Name)
	updated, err := service.Lifecycle.Update(c.Request.Context(), lifecycleUpdateForDisplayName(target.Key, name, c), target.Version)
	if err != nil {
		writeGroupError(c, err)
		return
	}
	recordManageAudit(c, "group.rename", map[string]any{"key": target.Key, "old_name": target.DisplayName, "new_name": updated.DisplayName})
	common.ApiSuccess(c, gin.H{"old_name": target.DisplayName, "new_name": updated.DisplayName, "key": target.Key})
}

// GetUserGroups keeps the v1 user-facing shape, keyed by group key because the
// old console writes the response keys straight into tokens.group. The key is
// also mirrored in "_key" for new clients.
func GetUserGroups(c *gin.Context) {
	service := model.CurrentGroupService()
	resolverManager := model.CurrentGroupResolver()
	if service == nil || resolverManager == nil {
		legacyGetUserGroups(c)
		return
	}
	userId := c.GetInt("id")
	userGroup, _ := model.GetUserGroup(userId, false)
	usableGroups := make(map[string]map[string]any)
	for key, description := range resolverManager.UsableGroups(c.Request.Context(), userGroup) {
		if service.Snapshots.Current().Group(key) == nil {
			continue
		}
		usableGroups[key] = map[string]any{
			"ratio": resolverManager.UserGroupRatio(c.Request.Context(), userGroup, key),
			"desc":  description,
			"_key":  key,
		}
	}
	// The auto pseudo group is not a registry row, but the frozen console's
	// token dropdown still expects it when the user may use auto.
	if resolverManager.IsSelectable(c.Request.Context(), userGroup, domain.AutoGroupTokenValue) {
		usableGroups[domain.AutoGroupTokenValue] = map[string]any{
			"ratio": "auto",
			"desc":  setting.GetUsableGroupDescription(domain.AutoGroupTokenValue),
			"_key":  domain.AutoGroupTokenValue,
		}
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": usableGroups})
}

func lifecycleUpdateForDisplayName(key, displayName string, c *gin.Context) (cmd lifecycle.UpdateGroup) {
	return lifecycle.UpdateGroup{Key: key, DisplayName: &displayName, Actor: currentGroupActor(c)}
}

// groupKeyToLegacyId maps a key to the numeric id the v1 console expects. The
// seeding step preserves legacy ids, so this is a straight lookup.
func groupKeyToLegacyId(service *model.GroupService, key string) int64 {
	id, err := service.Store.GetID(context.Background(), key)
	if err != nil {
		return 0
	}
	return id
}
