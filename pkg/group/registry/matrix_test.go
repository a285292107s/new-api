package registry_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/group/domain"
	"github.com/QuantumNous/new-api/pkg/group/registry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestThreeDatabaseMatrix runs the same registry contract against MySQL and
// PostgreSQL when the corresponding DSN environment variables are set. The
// plain `go test` run (SQLite) stays fast; CI or the staging host sets:
//
//	GROUP_TEST_MYSQL_DSN=user:pass@tcp(127.0.0.1:3306)/db?parseTime=true
//	GROUP_TEST_POSTGRES_DSN=host=127.0.0.1 port=5432 user=x password=y dbname=z sslmode=disable
func TestThreeDatabaseMatrix(t *testing.T) {
	cases := []struct {
		name string
		env  string
		open func(string) gorm.Dialector
	}{
		{name: "mysql", env: "GROUP_TEST_MYSQL_DSN", open: func(dsn string) gorm.Dialector { return mysql.Open(dsn) }},
		{name: "postgres", env: "GROUP_TEST_POSTGRES_DSN", open: func(dsn string) gorm.Dialector { return postgres.Open(dsn) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dsn := os.Getenv(tc.env)
			if dsn == "" {
				t.Skipf("%s not set", tc.env)
			}
			db, err := gorm.Open(tc.open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			require.NoError(t, err)
			runRegistryContract(t, db)
		})
	}
}

func runRegistryContract(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, model := range registry.AllModels() {
		require.NoError(t, db.Migrator().DropTable(model))
	}
	for _, model := range registry.AllModels() {
		require.NoError(t, db.AutoMigrate(model))
	}
	// Second AutoMigrate must be a no-op; this is the idempotency requirement
	// for every supported engine.
	for _, model := range registry.AllModels() {
		require.NoError(t, db.AutoMigrate(model))
	}

	store := registry.NewStore(db)
	ctx := context.Background()

	created, err := store.Create(ctx, domain.Group{
		Key:         "vip",
		DisplayName: "VIP 组",
		Status:      domain.StatusActive,
		Ratio:       0.8,
		TopupRatio:  1,
		Version:     1,
	})
	require.NoError(t, err)
	assert.Equal(t, "vip", created.Key)

	// Case-insensitive display uniqueness must work without a functional index.
	_, err = store.Create(ctx, domain.Group{Key: "vip2", DisplayName: "vip 组", Status: domain.StatusActive, Version: 1})
	require.ErrorIs(t, err, domain.ErrDisplayNameExists)

	// CAS update succeeds once, then fails on the stale version.
	next := *created
	next.DisplayName = "VIP Plus"
	next.Ratio = 0.7
	updated, err := store.Update(ctx, next, created.Version)
	require.NoError(t, err)
	assert.Equal(t, int64(2), updated.Version)
	_, err = store.Update(ctx, next, created.Version)
	require.ErrorIs(t, err, domain.ErrVersionConflict)

	// Relationship tables round-trip.
	require.NoError(t, store.ReplaceGroupGroupRatios(ctx, []domain.GroupGroupRatio{
		{UserGroupKey: "vip", UsingGroupKey: "default", Ratio: 0.9},
	}))
	ratios, err := store.ListGroupGroupRatios(ctx)
	require.NoError(t, err)
	require.Len(t, ratios, 1)

	// Idempotency key uniqueness across engines.
	record := registry.GroupChangeRecord{
		IdempotencyKey: "topup:99",
		UserId:         1,
		FromKey:        "default",
		ToKey:          "vip",
		Source:         "policy",
	}
	first, createdRow, err := store.AppendChangeRecord(ctx, record)
	require.NoError(t, err)
	assert.True(t, createdRow)
	second, createdRow, err := store.AppendChangeRecord(ctx, record)
	require.NoError(t, err)
	assert.False(t, createdRow)
	assert.Equal(t, first.Id, second.Id, "duplicate idempotency key must return the original row")

	// Meta version monotonicity.
	firstVersion, err := store.BumpVersion(ctx, nil)
	require.NoError(t, err)
	secondVersion, err := store.BumpVersion(ctx, nil)
	require.NoError(t, err)
	require.Greater(t, secondVersion, firstVersion)

	// Cleanup so the next matrix run starts from a clean schema.
	for _, model := range registry.AllModels() {
		require.NoError(t, db.Migrator().DropTable(model))
	}
	fmt.Println("matrix ok")
}

func init() {
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RedisEnabled = false
}
