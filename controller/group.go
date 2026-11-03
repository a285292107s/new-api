package controller

import (
	"errors"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
)

func GetGroups(c *gin.Context) {
	groupNames := make([]string, 0)
	for groupName := range ratio_setting.GetGroupRatioCopy() {
		groupNames = append(groupNames, groupName)
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    groupNames,
	})
}

type GroupRegistryItem struct {
	model.Group
	References map[string]int64 `json:"references"`
}

// GetGroupRegistry returns the normalized group registry together with the
// number of rows that still reference each group. The references column makes a
// rename auditable: it shows which tables the rename transaction will touch.
func GetGroupRegistry(c *gin.Context) {
	groups, err := model.ListGroups()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	items := make([]GroupRegistryItem, 0, len(groups))
	for _, group := range groups {
		references, err := model.CountGroupReferences(group.Name)
		if err != nil {
			common.ApiError(c, err)
			return
		}
		items = append(items, GroupRegistryItem{Group: group, References: references})
	}
	common.ApiSuccess(c, items)
}

type renameGroupRequest struct {
	Id   int    `json:"id"`
	Name string `json:"name"`
}

// RenameGroup renames a group atomically. All legacy string projections are
// rewritten in one database transaction so no server restart or manual sweep is
// required.
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
	oldName, err := model.RenameGroup(request.Id, request.Name)
	if err != nil {
		switch {
		case errors.Is(err, model.ErrGroupNotFound):
			common.ApiErrorT(c, "Group not found")
		case errors.Is(err, model.ErrGroupNameExists):
			common.ApiErrorT(c, "Group name already exists")
		case errors.Is(err, model.ErrBuiltinGroup):
			common.ApiErrorT(c, "Default group cannot be renamed")
		case errors.Is(err, model.ErrGroupNameReserved):
			common.ApiErrorT(c, "Group name is reserved")
		default:
			common.ApiError(c, err)
		}
		return
	}
	common.ApiSuccess(c, gin.H{
		"old_name": oldName,
		"new_name": strings.TrimSpace(request.Name),
	})
}

func GetUserGroups(c *gin.Context) {
	usableGroups := make(map[string]map[string]any)
	userGroup := ""
	userId := c.GetInt("id")
	userGroup, _ = model.GetUserGroup(userId, false)
	userUsableGroups := service.GetUserUsableGroups(userGroup)
	for groupName, _ := range ratio_setting.GetGroupRatioCopy() {
		// UserUsableGroups contains the groups that the user can use
		if desc, ok := userUsableGroups[groupName]; ok {
			usableGroups[groupName] = map[string]any{
				"ratio": service.GetUserGroupRatio(userGroup, groupName),
				"desc":  desc,
			}
		}
	}
	if _, ok := userUsableGroups["auto"]; ok {
		usableGroups["auto"] = map[string]any{
			"ratio": "auto",
			"desc":  setting.GetUsableGroupDescription("auto"),
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    usableGroups,
	})
}
