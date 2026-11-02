package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetUserLogsPageSkipTotal verifies the user log list can poll without
// paying for the full-range COUNT(*): skip_total returns total=-1 but the same
// rows, while the normal path still reports the real total.
func TestGetUserLogsPageSkipTotal(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.Exec("DELETE FROM logs").Error)

	now := time.Now().Unix()
	for i := 0; i < 3; i++ {
		require.NoError(t, DB.Create(&Log{
			UserId:    7,
			CreatedAt: now - int64(i),
			Type:      LogTypeConsume,
			Quota:     100 + i,
			Username:  "alice",
		}).Error)
	}

	logs, total, err := GetUserLogsPage(7, LogFilter{Type: LogTypeConsume}, 0, 2, true)
	require.NoError(t, err)
	assert.Equal(t, int64(-1), total, "skip_total must not run COUNT and reports -1")
	assert.Len(t, logs, 2, "rows are still paginated normally")

	logs, total, err = GetUserLogsPage(7, LogFilter{Type: LogTypeConsume}, 0, 2, false)
	require.NoError(t, err)
	assert.Equal(t, int64(3), total)
	assert.Len(t, logs, 2)
}

// TestSumUsedQuotaByUserId verifies the single-round-trip user stat query keeps
// the documented semantics: quota follows the requested range, rpm/tpm always
// use the last 60 seconds, and filtering is by user id (stable across renames).
func TestSumUsedQuotaByUserId(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.Exec("DELETE FROM logs").Error)

	now := time.Now().Unix()
	seed := []Log{
		// Recent consume log: counts toward both range quota and last-60s rpm/tpm.
		{UserId: 7, Username: "renamed-alice", CreatedAt: now - 10, Type: LogTypeConsume, Quota: 100, PromptTokens: 10, CompletionTokens: 5},
		// Older consume log: only counts toward range quota.
		{UserId: 7, Username: "renamed-alice", CreatedAt: now - 300, Type: LogTypeConsume, Quota: 200, PromptTokens: 20, CompletionTokens: 10},
		// Other user: must not leak into user 7 stats.
		{UserId: 8, Username: "bob", CreatedAt: now - 10, Type: LogTypeConsume, Quota: 900, PromptTokens: 90, CompletionTokens: 90},
		// Non-consume log: must be ignored.
		{UserId: 7, Username: "renamed-alice", CreatedAt: now - 10, Type: LogTypeTopup, Quota: 500},
	}
	for i := range seed {
		require.NoError(t, DB.Create(&seed[i]).Error)
	}

	stat, err := SumUsedQuotaByUserId(7, now-3600, 0, "", "", 0, "")
	require.NoError(t, err)
	assert.Equal(t, 300, stat.Quota)
	assert.Equal(t, 1, stat.Rpm)
	assert.Equal(t, 15, stat.Tpm)

	// A range that ends before "now" still reports the last 60 seconds for rpm/tpm,
	// matching SumUsedQuota's existing behaviour.
	stat, err = SumUsedQuotaByUserId(7, now-3600, now-60, "", "", 0, "")
	require.NoError(t, err)
	assert.Equal(t, 200, stat.Quota)
	assert.Equal(t, 1, stat.Rpm)
	assert.Equal(t, 15, stat.Tpm)
}

// TestLogCompositeIndexesExist guards the index declarations the incremental
// polling depends on: without (user_id, created_at) the windowed probe and
// COUNT queries fall back to scanning the user's entire log history.
func TestLogCompositeIndexesExist(t *testing.T) {
	var names []string
	require.NoError(t, DB.Raw("SELECT name FROM sqlite_master WHERE type = 'index' AND tbl_name IN ('logs', 'quota_data')").Scan(&names).Error)
	for _, want := range []string{"idx_logs_user_created", "idx_logs_username_type_created", "idx_quota_data_user_created"} {
		assert.Contains(t, names, want)
	}
}
