package model

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTokenAutoGroupsRoundTripThroughRedisHashCache(t *testing.T) {
	useUserCacheMiniRedis(t)
	token := Token{
		Id:         42,
		UserId:     7,
		Key:        "token-auto-groups-cache-key",
		Name:       "auto-cache",
		Group:      "auto",
		AutoGroups: `["vip","default"]`,
	}

	require.NoError(t, cacheSetTokenForTest(token))
	cached, err := cacheGetTokenByKey(token.Key)
	require.NoError(t, err)
	assert.Equal(t, token.AutoGroups, cached.AutoGroups)
	groups, err := cached.GetAutoGroups()
	require.NoError(t, err)
	assert.Equal(t, []string{"vip", "default"}, groups)
}

func TestTokenUpdateSynchronouslyNarrowsPreheatedAutoGroupsCache(t *testing.T) {
	truncateTables(t)
	useUserCacheMiniRedis(t)
	token := Token{
		UserId:          7,
		Key:             "token-auto-groups-update-cache-key",
		Name:            "auto-cache-update",
		Status:          common.TokenStatusEnabled,
		ExpiredTime:     -1,
		UnlimitedQuota:  true,
		Group:           "auto",
		CrossGroupRetry: true,
		AutoGroups:      `["default","vip"]`,
	}
	require.NoError(t, token.Insert())
	require.NoError(t, cacheSetTokenForTest(token))

	preheated, err := cacheGetTokenByKey(token.Key)
	require.NoError(t, err)
	assert.JSONEq(t, `["default","vip"]`, preheated.AutoGroups)

	require.NoError(t, token.SetAutoGroups([]string{"vip"}))
	require.NoError(t, token.Update())
	// Update 是限制性变更：写库前删除缓存并设置 fence。缓存不再提供旧的
	// 宽分组值，下一次读取必须看到收紧后的分组。
	_, cacheErr := cacheGetTokenByKey(token.Key)
	require.Error(t, cacheErr, "the pre-update cache entry must be invalidated")
	reloaded, err := GetTokenByKey(token.Key, false)
	require.NoError(t, err)
	assert.JSONEq(t, `["vip"]`, reloaded.AutoGroups)
}

// cacheSetTokenForTest 以测试身份写入完整 token 缓存（含额度字段），
// 模拟“已水合”的缓存状态。
func cacheSetTokenForTest(token Token) error {
	_, err := cacheInitToken(token)
	return err
}

func TestTokenBoundGroupsCacheAndLegacyHydration(t *testing.T) {
	truncateTables(t)
	resetBatchUpdateTestState(t)
	server := useUserCacheMiniRedis(t)
	token := createReserveTestToken(t, 100)
	token.Groups = NewTokenGroups([]string{"image", "video", "audio"})
	token.Group = "image"
	require.NoError(t, token.Update())
	server.FastForward(time.Duration(tokenCacheFenceSeconds+1) * time.Second)

	for range 3 {
		loaded, err := GetTokenByKey(token.Key, false)
		require.NoError(t, err)
		assert.Equal(t, []string{"image", "video", "audio"}, loaded.GetGroups())
	}
	// Simulate a pre-fix cache and a reservation newer than the DB snapshot.
	require.NoError(t, common.RDB.HDel(t.Context(), getTokenCacheKey(token.Key), "Groups").Err())
	result, err := cacheApplyTokenQuotaDelta(token.Id, token.Key, -70)
	require.NoError(t, err)
	require.Equal(t, cacheQuotaOK, result)
	_, err = cacheGetTokenByKey(token.Key)
	require.Error(t, err, "a legacy hash must fall back to the database, not one group")
	loaded, err := GetTokenByKey(token.Key, false)
	require.NoError(t, err)
	assert.Equal(t, token.GetGroups(), loaded.GetGroups())
	cached, err := cacheGetTokenByKey(token.Key)
	require.NoError(t, err)
	assert.Equal(t, token.GetGroups(), cached.GetGroups())
	assert.Equal(t, 30, cached.RemainQuota, "metadata hydration must not undo pre-consumption")

	// A narrowing update fences stale snapshots and removes revoked groups.
	stale := token
	token.Groups = NewTokenGroups([]string{"video"})
	token.Group = "video"
	require.NoError(t, token.Update())
	code, err := cacheInitToken(stale)
	require.NoError(t, err)
	assert.Zero(t, code)
	server.FastForward(time.Duration(tokenCacheFenceSeconds+1) * time.Second)
	for range 3 {
		loaded, err = GetTokenByKey(token.Key, false)
		require.NoError(t, err)
		assert.Equal(t, []string{"video"}, loaded.GetGroups())
	}

	// Legacy single-group and account-inherited tokens are still valid hashes.
	for _, group := range []string{"image", ""} {
		single := Token{Id: 999, Key: "legacy-single-" + group, Group: group}
		require.NoError(t, cacheSetTokenForTest(single))
		cached, err := cacheGetTokenByKey(single.Key)
		require.NoError(t, err)
		assert.Equal(t, single.GetGroups(), cached.GetGroups())
	}
}
