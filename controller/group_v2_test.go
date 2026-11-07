package controller

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/group/lifecycle"
	"github.com/QuantumNous/new-api/pkg/group/registry"
	"github.com/QuantumNous/new-api/pkg/group/resolver"
	"github.com/QuantumNous/new-api/pkg/group/snapshot"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// newGroupTestService installs a group service on a dedicated in-memory
// database. Controller tests share the process, so this never touches any
// database another test may have installed.
func newGroupTestService(t *testing.T) *model.GroupService {
	t.Helper()
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })

	for _, m := range registry.AllModels() {
		require.NoError(t, db.AutoMigrate(m))
	}
	require.NoError(t, db.AutoMigrate(&model.User{}))

	store := registry.NewStore(db)
	snaps := snapshot.NewManager(store)
	_, err = snaps.Rebuild(context.Background())
	require.NoError(t, err)
	service := &model.GroupService{Store: store, Snapshots: snaps}
	// Archive guards need the reference adapters; use the production wiring on
	// the test database so the hard-reference check is real.
	service.Lifecycle = model.NewLifecycleForTest(service)
	// Assignment tests need a real applier; wiring it without a reference
	// checker is fine because assignment never archives anything.
	service.Assigner = model.NewAssignerForTest(service)
	model.InstallGroupServiceForTest(service, resolver.NewManager(snaps))
	t.Cleanup(func() { model.InstallGroupServiceForTest(nil, nil) })

	previousRedis := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = previousRedis })

	// The audit writer looks the operator up by id; give every handler test a
	// real admin row so it can record who performed the change.
	admin := model.User{Username: "group-test-admin", Password: "unused", Role: common.RoleRootUser, Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, AffCode: "GTA"}
	require.NoError(t, db.Create(&admin).Error)
	t.Cleanup(func() { model.DB = previousDB })
	return service
}

func cleanGroupTablesInControllerTest() {
	for _, table := range []string{
		"group_registry", "group_group_ratio", "group_visibility_rule", "group_rate_limit",
		"group_change_record", "group_event", "group_meta", "group_shadow_diff",
	} {
		model.DB.Exec("DELETE FROM " + table)
	}
}

func createTestGroup(t *testing.T, service *model.GroupService, key, display string, ratio float64) *struct {
	Key         string
	DisplayName string
	Ratio       float64
	Version     int64
} {
	t.Helper()
	group, err := service.Lifecycle.Create(context.Background(), lifecycle.CreateGroup{
		Key:         key,
		DisplayName: display,
		Ratio:       ratio,
		TopupRatio:  1,
	})
	require.NoError(t, err)
	return &struct {
		Key         string
		DisplayName string
		Ratio       float64
		Version     int64
	}{Key: group.Key, DisplayName: group.DisplayName, Ratio: group.Ratio, Version: group.Version}
}

func jsonRequest(method, path string, body []byte) (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("id", 1)
	ctx.Set("role", common.RoleRootUser)
	ctx.Set("username", "group-test-admin")
	ctx.Request = httptest.NewRequest(method, path, bytes.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	return ctx, recorder
}

func TestGetGroupsV2ReturnsStructuredPayload(t *testing.T) {
	service := newGroupTestService(t)
	createTestGroup(t, service, "vip", "VIP 组", 0.8)

	ctx, recorder := jsonRequest(http.MethodGet, "/api/v2/groups/", nil)
	GetGroupsV2(ctx)
	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"key":"vip"`)
	assert.Contains(t, recorder.Body.String(), `"display_name":"VIP 组"`)
	assert.Contains(t, recorder.Body.String(), `"ratio":0.8`)
}

func TestCreateGroupV2RejectsDuplicateKey(t *testing.T) {
	service := newGroupTestService(t)
	createTestGroup(t, service, "vip", "VIP", 1)

	ctx, recorder := jsonRequest(http.MethodPost, "/api/v2/groups/", []byte(`{"key":"vip","display_name":"VIP2","ratio":1}`))
	CreateGroupV2(ctx)
	assert.Equal(t, http.StatusConflict, recorder.Code)
}

func TestUpdateGroupV2RequiresMatchingVersion(t *testing.T) {
	service := newGroupTestService(t)
	created := createTestGroup(t, service, "vip", "VIP", 1)

	body := []byte(`{"display_name":"VIP Plus","expected_version":` + strconv.FormatInt(created.Version, 10) + `}`)
	ctx, recorder := jsonRequest(http.MethodPatch, "/api/v2/groups/vip", body)
	ctx.Params = gin.Params{{Key: "key", Value: "vip"}}
	UpdateGroupV2(ctx)
	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "VIP Plus")

	// The stale version must be rejected rather than silently overwriting.
	ctx2, recorder2 := jsonRequest(http.MethodPatch, "/api/v2/groups/vip", []byte(`{"display_name":"Racer","expected_version":1}`))
	ctx2.Params = gin.Params{{Key: "key", Value: "vip"}}
	UpdateGroupV2(ctx2)
	assert.Equal(t, http.StatusConflict, recorder2.Code)
}

func TestArchiveGroupV2RejectedByReferences(t *testing.T) {
	service := newGroupTestService(t)
	created := createTestGroup(t, service, "vip", "VIP", 1)
	require.NoError(t, model.DB.Create(&model.User{Username: "ref-user", Password: "x", AffCode: "REF", Group: "vip"}).Error)
	t.Cleanup(func() { model.DB.Where("username = ?", "ref-user").Delete(&model.User{}) })

	ctx, recorder := jsonRequest(http.MethodPost, "/api/v2/groups/vip/archive", []byte(`{"expected_version":`+strconv.FormatInt(created.Version, 10)+`}`))
	ctx.Params = gin.Params{{Key: "key", Value: "vip"}}
	ArchiveGroupV2(ctx)
	assert.Equal(t, http.StatusConflict, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "references")
}

func TestGetGroupV2UnknownKeyIs404(t *testing.T) {
	newGroupTestService(t)
	ctx, recorder := jsonRequest(http.MethodGet, "/api/v2/groups/ghost", nil)
	ctx.Params = gin.Params{{Key: "key", Value: "ghost"}}
	GetGroupV2(ctx)
	assert.Equal(t, http.StatusNotFound, recorder.Code)
}

func TestAssignGroupV2IsIdempotent(t *testing.T) {
	service := newGroupTestService(t)
	createTestGroup(t, service, "vip", "VIP", 1)
	require.NoError(t, model.DB.Create(&model.User{Username: "assign-user", Password: "x", AffCode: "ASN", Group: "default"}).Error)
	var user model.User
	require.NoError(t, model.DB.Where("username = ?", "assign-user").First(&user).Error)
	t.Cleanup(func() { model.DB.Where("username = ?", "assign-user").Delete(&model.User{}) })

	body := []byte(`{"user_id":` + strconv.Itoa(user.Id) + `,"key":"vip","reason":"test","idempotency_key":"ctrl-test:1"}`)
	for range 2 {
		ctx, recorder := jsonRequest(http.MethodPost, "/api/v2/groups/vip/assign", body)
		ctx.Params = gin.Params{{Key: "key", Value: "vip"}}
		AssignGroupV2(ctx)
		require.Equal(t, http.StatusOK, recorder.Code)
	}
	var count int64
	require.NoError(t, model.DB.Model(&registry.GroupChangeRecord{}).Where("idempotency_key = ?", "ctrl-test:1").Count(&count).Error)
	assert.EqualValues(t, 1, count)

	var reloaded model.User
	require.NoError(t, model.DB.Where("id = ?", user.Id).First(&reloaded).Error)
	assert.Equal(t, "vip", reloaded.Group)
}

