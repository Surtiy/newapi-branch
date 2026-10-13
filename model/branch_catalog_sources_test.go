package model

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBranchReconcileVerifiedGroupsAreNotInferredFromModels(t *testing.T) {
	setupBranchTestDB(t)
	id, err := SaveBranchCatalogGroupsKey(0, []string{"verified-a", "verified-b"}, "verified-test-key")
	require.NoError(t, err)
	t.Cleanup(func() { DB.Where("id = ?", id).Delete(&Channel{}) })
	var source Channel
	require.NoError(t, DB.First(&source, id).Error)
	bound := []string{"verified-a", "verified-b"}
	_, err = ReconcileBranchCatalogSource(&source, []BranchCatalogModelScope{{ModelName: "a-only", Groups: []string{"verified-a"}}}, bound)
	require.NoError(t, err)
	require.Equal(t, bound, BranchCatalogBoundGroups(&source), "a bound group without published models still belongs to the token")
	version := BranchCatalogSourceVersion(&source)
	_, err = ReconcileBranchCatalogSource(&source, []BranchCatalogModelScope{{ModelName: "outside", Groups: []string{"outside"}}}, bound)
	require.Error(t, err)
	require.Equal(t, version, BranchCatalogSourceVersion(&source))
	_, err = ReconcileBranchCatalogSource(&source, nil, bound)
	require.NoError(t, err)
	require.Equal(t, bound, BranchCatalogBoundGroups(&source))
	_, err = ReconcileBranchCatalogSource(&source, nil, []string{})
	require.NoError(t, err)
	require.Empty(t, BranchCatalogBoundGroups(&source))
}

func TestBranchReconcileSourceRemovesOnlyWithdrawnRoutes(t *testing.T) {
	setupBranchTestDB(t)
	require.NoError(t, DB.AutoMigrate(&Task{}))
	id, err := SaveBranchCatalogGroupsKey(0, []string{"refresh-a", "refresh-b"}, "refresh-key")
	require.NoError(t, err)
	var source Channel
	require.NoError(t, DB.First(&source, id).Error)
	ids := []int{id}
	t.Cleanup(func() {
		DB.Where("channel_id IN ?", ids).Delete(&Ability{})
		DB.Where("id IN ?", ids).Delete(&Channel{})
	})
	children := make([]Channel, 0, 3)
	for _, spec := range []struct {
		group, models string
		video         bool
	}{
		{"refresh-a", "kept,withdrawn", false},
		{"refresh-a", "video-kept,video-withdrawn", true},
		{"refresh-b", "group-withdrawn", false},
	} {
		tag := BranchGroupChannelTag(id, spec.group, spec.video)
		kind := 1
		if spec.video {
			kind = constant.ChannelTypeTaskPlugin
		}
		setting := `{"task_plugin_key":"aicost-branch"}`
		child := Channel{Type: kind, Key: source.Key, BaseURL: source.BaseURL, Group: spec.group, Tag: &tag, Setting: &setting, Models: spec.models, Status: 1}
		require.NoError(t, DB.Create(&child).Error)
		require.NoError(t, child.AddAbilities(nil))
		ids = append(ids, child.Id)
		children = append(children, child)
	}
	task := Task{TaskID: "refresh-old-task", ChannelId: children[1].Id, Status: TaskStatusInProgress, Quota: 123, PrivateData: TaskPrivateData{UpstreamTaskID: "refresh-upstream"}}
	require.NoError(t, DB.Create(&task).Error)
	t.Cleanup(func() { DB.Where("id = ?", task.ID).Delete(&Task{}) })
	prices := common.OptionMap["ModelPrice"]
	scopes := []BranchCatalogModelScope{{ModelName: "kept", Groups: []string{"refresh-a"}}, {ModelName: "video-kept", Groups: []string{"refresh-a"}, Video: true}, {ModelName: "new-model", Groups: []string{"refresh-a"}}}
	result, err := ReconcileBranchCatalogSource(&source, scopes)
	require.NoError(t, err)
	assert.Equal(t, []string{"refresh-a"}, BranchCatalogBoundGroups(&source))
	assert.Equal(t, 3, result.RemovedModels)
	for index, want := range []string{"kept", "video-kept", "group-withdrawn"} {
		row, err := GetChannelById(children[index].Id, true)
		require.NoError(t, err)
		assert.Equal(t, want, row.Models)
		assert.Equal(t, "refresh-key", row.Key)
		if index == 2 {
			assert.Equal(t, common.ChannelStatusManuallyDisabled, row.Status)
			assert.True(t, strings.HasPrefix(row.GetTag(), "branch-retired-"))
		}
	}
	var abilities []Ability
	require.NoError(t, DB.Where("channel_id IN ? AND enabled = ?", ids, true).Find(&abilities).Error)
	assert.Len(t, abilities, 2)
	var persisted Task
	require.NoError(t, DB.First(&persisted, task.ID).Error)
	assert.Equal(t, task, persisted)
	assert.Equal(t, prices, common.OptionMap["ModelPrice"])
	result, err = ReconcileBranchCatalogSource(&source, scopes)
	require.NoError(t, err)
	assert.Zero(t, result.RemovedModels)
	result, err = ReconcileBranchCatalogSource(&source, nil)
	require.NoError(t, err)
	assert.Equal(t, 2, result.RemovedModels)
	assert.Empty(t, BranchCatalogBoundGroups(&source))
	assert.True(t, IsBranchCatalogSource(&source))
	require.NoError(t, DB.Where("channel_id IN ? AND enabled = ?", ids, true).Find(&abilities).Error)
	assert.Empty(t, abilities)
	_, err = DeleteBranchCatalogSource(id, BranchCatalogSourceVersion(&source))
	require.NoError(t, err)
}

func TestBranchReconcileRejectsStaleSourceWithoutPruning(t *testing.T) {
	setupBranchTestDB(t)
	id, err := SaveBranchCatalogGroupKey(0, "refresh-stale", "original-key")
	require.NoError(t, err)
	t.Cleanup(func() { DB.Where("id = ?", id).Delete(&Channel{}) })
	var source Channel
	require.NoError(t, DB.First(&source, id).Error)
	require.NoError(t, DB.Model(&Channel{}).Where("id = ?", id).Update("key", "rotated-key").Error)
	_, err = ReconcileBranchCatalogSource(&source, nil)
	require.Error(t, err)
	var persisted Channel
	require.NoError(t, DB.First(&persisted, id).Error)
	assert.Equal(t, "rotated-key", persisted.Key)
	assert.Equal(t, []string{"refresh-stale"}, BranchCatalogBoundGroups(&persisted))
	_, err = ReconcileBranchCatalogSource(&persisted, []BranchCatalogModelScope{{ModelName: "withdrawn", Groups: []string{"refresh-stale"}}})
	require.NoError(t, err)
	stale := persisted
	_, err = ReconcileBranchCatalogSource(&persisted, []BranchCatalogModelScope{{ModelName: "replacement", Groups: []string{"refresh-stale"}}})
	require.NoError(t, err)
	snapshot, err := GetModelPricingSnapshot([]string{"withdrawn"})
	require.NoError(t, err)
	states, err := GetBranchGroupStates([]string{"refresh-stale"})
	require.NoError(t, err)
	item := BranchModelImport{Change: ModelPricingChange{ModelName: "withdrawn", ExpectedVersion: snapshot.Entries[0].Version, Pricing: PricingValues{"ModelPrice": 1.0}}, UpdatePrice: true, Metadata: Model{ModelName: "withdrawn", Endpoints: `["image-generation"]`}, Groups: []string{"refresh-stale"}}
	require.ErrorContains(t, ImportBranchModels(&stale, []BranchModelImport{item}, BranchGroupSync{Name: "refresh-stale", Ratio: 1, ExpectedVersion: states["refresh-stale"].Version}), "渠道配置已变化")
	var count int64
	require.NoError(t, DB.Model(&Ability{}).Where("model = ?", "withdrawn").Count(&count).Error)
	assert.Zero(t, count, "a stale apply must not resurrect withdrawn models")
}

func TestBranchDeleteSourceRetainsTasksAndReleasesGroups(t *testing.T) {
	setupBranchTestDB(t)
	require.NoError(t, DB.AutoMigrate(&Task{}))
	id, err := SaveBranchCatalogGroupsKey(0, []string{"delete-a", "delete-b"}, "delete-original-key")
	require.NoError(t, err)
	var source Channel
	require.NoError(t, DB.First(&source, id).Error)
	ids := []int{id}
	t.Cleanup(func() {
		DB.Where("channel_id IN ?", ids).Delete(&Ability{})
		DB.Where("id IN ?", ids).Delete(&Channel{})
	})
	base := BranchMainURL
	var children []Channel
	for _, video := range []bool{false, true} {
		tag := BranchGroupChannelTag(id, "delete-a", video)
		kind := 1
		if video {
			kind = constant.ChannelTypeTaskPlugin
		}
		setting := `{"task_plugin_key":"aicost-branch"}`
		child := Channel{Name: "retained", Type: kind, Group: "delete-a", Key: source.Key, BaseURL: &base, Tag: &tag, Setting: &setting, Models: "retained-model", Status: common.ChannelStatusEnabled}
		require.NoError(t, DB.Create(&child).Error)
		require.NoError(t, child.AddAbilities(nil))
		children = append(children, child)
		ids = append(ids, child.Id)
	}
	unrelated := Channel{Name: "unrelated", Type: 1, Group: "delete-a", BaseURL: &base, Key: "unrelated-key", Status: 1}
	require.NoError(t, DB.Create(&unrelated).Error)
	ids = append(ids, unrelated.Id)
	task := Task{TaskID: "delete-task", ChannelId: children[1].Id, Status: TaskStatusInProgress, Quota: 1234, PrivateData: TaskPrivateData{UpstreamTaskID: "retained-upstream"}}
	require.NoError(t, DB.Create(&task).Error)
	t.Cleanup(func() { DB.Where("id = ?", task.ID).Delete(&Task{}) })
	priceBefore := common.OptionMap["ModelPrice"]
	groupBefore := common.OptionMap["GroupRatio"]
	disabled, err := DeleteBranchCatalogSource(id, BranchCatalogSourceVersion(&source))
	require.NoError(t, err)
	assert.Equal(t, 2, disabled)
	var count int64
	require.NoError(t, DB.Model(&Channel{}).Where("id = ?", id).Count(&count).Error)
	assert.Zero(t, count)
	for _, before := range children {
		after, err := GetChannelById(before.Id, true)
		require.NoError(t, err)
		assert.Equal(t, common.ChannelStatusManuallyDisabled, after.Status)
		assert.Equal(t, before.Key, after.Key)
		assert.Equal(t, before.Models, after.Models)
		assert.Equal(t, before.GetSetting(), after.GetSetting())
		assert.True(t, strings.HasPrefix(after.GetTag(), "branch-retired-"))
		require.NoError(t, DB.Model(&Ability{}).Where("channel_id = ? AND enabled = ?", before.Id, true).Count(&count).Error)
		assert.Zero(t, count)
	}
	var persisted Task
	require.NoError(t, DB.First(&persisted, task.ID).Error)
	assert.Equal(t, task.Status, persisted.Status)
	assert.Equal(t, task.Quota, persisted.Quota)
	assert.Equal(t, task.PrivateData, persisted.PrivateData)
	require.NoError(t, DB.First(&unrelated, unrelated.Id).Error)
	assert.Equal(t, 1, unrelated.Status)
	assert.Equal(t, priceBefore, common.OptionMap["ModelPrice"])
	assert.Equal(t, groupBefore, common.OptionMap["GroupRatio"])
	newID, err := SaveBranchCatalogGroupsKey(0, []string{"delete-a", "delete-b"}, "new-source-key")
	require.NoError(t, err)
	assert.NotEqual(t, id, newID)
	ids = append(ids, newID)
	for _, child := range children {
		after, err := GetChannelById(child.Id, true)
		require.NoError(t, err)
		assert.Equal(t, "delete-original-key", after.Key, "re-adding must not rotate old task credentials")
	}
}

func TestBranchDeleteSourceRejectsStaleAndCustomizedBindings(t *testing.T) {
	for _, change := range []string{"key", "child", "legacy"} {
		t.Run(change, func(t *testing.T) {
			setupBranchTestDB(t)
			id, err := SaveBranchCatalogGroupsKey(0, []string{"delete-conflict"}, "original-key")
			require.NoError(t, err)
			var source Channel
			require.NoError(t, DB.First(&source, id).Error)
			version := BranchCatalogSourceVersion(&source)
			tag := BranchGroupChannelTag(id, source.Group, false)
			child := Channel{Name: "child", Type: 1, Group: source.Group, Key: source.Key, BaseURL: source.BaseURL, Tag: &tag, Status: 1}
			require.NoError(t, DB.Create(&child).Error)
			t.Cleanup(func() { DB.Where("id IN ?", []int{id, child.Id}).Delete(&Channel{}) })
			switch change {
			case "key":
				require.NoError(t, DB.Model(&source).Update("key", "new-key").Error)
			case "child":
				require.NoError(t, DB.Model(&child).Update("key", "custom-key").Error)
			case "legacy":
				require.NoError(t, DB.Model(&source).Update("tag", nil).Error)
			}
			disabled, err := DeleteBranchCatalogSource(id, version)
			require.ErrorIs(t, err, ErrBranchCatalogDeleteConflict)
			assert.Zero(t, disabled)
			require.NoError(t, DB.First(&source, id).Error)
			require.NoError(t, DB.First(&child, child.Id).Error)
			assert.Equal(t, 1, child.Status)
			assert.Equal(t, tag, child.GetTag())
		})
	}
}

func TestBranchKeyAllowsSharedGroupsButRejectsRepeatedCredential(t *testing.T) {
	setupBranchTestDB(t)
	a, err := SaveBranchCatalogGroupsKey(0, []string{"a", "b"}, "sk-aaaa-private-1111")
	require.NoError(t, err)
	b, err := SaveBranchCatalogGroupsKey(0, []string{"c"}, "sk-bbbb-private-2222")
	require.NoError(t, err)
	t.Cleanup(func() { DB.Where("id IN ?", []int{a, b}).Delete(&Channel{}) })
	c, err := SaveBranchCatalogGroupsKey(0, []string{"a", "b", "c", "new"}, "sk-cccc-private-3333")
	require.NoError(t, err)
	t.Cleanup(func() { DB.Where("id = ?", c).Delete(&Channel{}) })
	_, err = SaveBranchCatalogGroupsKey(0, []string{"a", "b"}, "sk-bbbb-private-2222")
	var conflictError *BranchCatalogKeyConflictError
	require.True(t, errors.As(err, &conflictError))
	require.Equal(t, []BranchCatalogKeyConflict{
		{ChannelID: b, Groups: []string{"c"}, DuplicateKey: true},
	}, conflictError.Conflicts)
	require.NotContains(t, err.Error(), "private")
	for id, key := range map[int]string{a: "sk-aaaa-private-1111", b: "sk-bbbb-private-2222"} {
		var row Channel
		require.NoError(t, DB.First(&row, id).Error)
		require.Equal(t, key, row.Key)
	}
	var count int64
	require.NoError(t, DB.Model(&Channel{}).Where("id IN ?", []int{a, b, c}).Count(&count).Error)
	require.EqualValues(t, 3, count)
}

func TestBranchKeyAddCannotOverwriteAnIdenticalGroupScope(t *testing.T) {
	setupBranchTestDB(t)
	id, err := SaveBranchCatalogGroupsKey(0, []string{"a", "b"}, "original-key")
	require.NoError(t, err)
	t.Cleanup(func() { DB.Where("id = ?", id).Delete(&Channel{}) })
	second, err := SaveBranchCatalogGroupsKey(0, []string{"b", "a"}, "second-key")
	require.NoError(t, err)
	require.NotEqual(t, id, second)
	t.Cleanup(func() { DB.Where("id = ?", second).Delete(&Channel{}) })
	var row Channel
	require.NoError(t, DB.First(&row, id).Error)
	require.Equal(t, "original-key", row.Key)
	repeated, err := SaveBranchCatalogGroupsKey(0, []string{"a", "b"}, "original-key")
	require.NoError(t, err)
	require.Equal(t, id, repeated)
	rotated, err := SaveBranchCatalogGroupsKey(id, []string{"a", "b"}, "replacement-key")
	require.NoError(t, err)
	require.Equal(t, id, rotated)
}

func TestBranchMultiGroupKeyMigrationAndRotation(t *testing.T) {
	setupBranchTestDB(t)
	base := BranchMainURL
	ids := []int{}
	t.Cleanup(func() {
		var rows []Channel
		DB.Where("id IN ? OR name IN ?", ids, []string{"multi-unique-model-0", "multi-unique-model-1"}).Find(&rows)
		for _, row := range rows {
			DB.Where("channel_id = ?", row.Id).Delete(&Ability{})
			DB.Delete(&row)
		}
		for _, id := range ids {
			DB.Where("tag LIKE ?", fmt.Sprintf("branch-group-%d-%%", id)).Find(&rows)
			for _, row := range rows {
				DB.Where("channel_id = ?", row.Id).Delete(&Ability{})
				DB.Delete(&row)
			}
		}
	})
	groups := []string{"multi-a-" + strings.Repeat("a", 40), "multi-b-" + strings.Repeat("b", 40)}
	var children []Channel
	for _, group := range groups {
		tag := BranchGroupChannelTag(999, group, false)
		child := Channel{Name: "existing", Group: group, Tag: &tag, Type: 1, Key: "old", BaseURL: &base, Models: "kept-model", Status: 1}
		require.NoError(t, DB.Create(&child).Error)
		children = append(children, child)
		ids = append(ids, child.Id)
	}
	id, err := SaveBranchCatalogGroupsKey(0, []string{groups[1], groups[0], groups[0]}, "multi-key")
	require.NoError(t, err)
	ids = append(ids, id)
	var source Channel
	require.NoError(t, DB.First(&source, id).Error)
	require.Equal(t, groups, BranchCatalogBoundGroups(&source))
	require.LessOrEqual(t, len(source.Group), 64)
	rotated, err := SaveBranchCatalogGroupsKey(id, groups, "rotated-multi-key")
	require.NoError(t, err)
	require.Equal(t, id, rotated)
	for _, before := range children {
		var child Channel
		require.NoError(t, DB.First(&child, before.Id).Error)
		require.Equal(t, before.Key, child.Key)
		require.Equal(t, before.Models, child.Models)
		require.Equal(t, before.GetTag(), child.GetTag())
	}
	other, err := SaveBranchCatalogGroupsKey(0, []string{groups[0], "unrelated"}, "other-key")
	require.NoError(t, err)
	ids = append(ids, other)
	t.Cleanup(func() { DB.Where("id = ?", other).Delete(&Channel{}) })
	_, err = SaveBranchCatalogGroupsKey(id, []string{groups[0]}, "changed-groups")
	require.Error(t, err)
	require.NoError(t, DB.First(&source, id).Error)
	require.Equal(t, "rotated-multi-key", source.Key)
	for index, group := range groups {
		name := fmt.Sprintf("multi-unique-model-%d", index)
		snapshot, err := GetModelPricingSnapshot([]string{name})
		require.NoError(t, err)
		states, err := GetBranchGroupStates([]string{group})
		require.NoError(t, err)
		item := BranchModelImport{Change: ModelPricingChange{ModelName: name, ExpectedVersion: snapshot.Entries[0].Version, Pricing: PricingValues{"ModelPrice": 0.5}}, UpdatePrice: true, Metadata: Model{ModelName: name, Endpoints: `["image-generation"]`}, Groups: []string{group}}
		require.NoError(t, ImportBranchModels(&source, []BranchModelImport{item}, BranchGroupSync{Name: group, Ratio: 1, ExpectedVersion: states[group].Version}))
	}
}

func TestBranchSingleGroupKeysIsolationAndMigration(t *testing.T) {
	setupBranchTestDB(t)
	base := BranchMainURL
	ids := []int{}
	t.Cleanup(func() {
		for _, id := range ids {
			var children []Channel
			DB.Where("tag LIKE ?", fmt.Sprintf("branch-group-%d-%%", id)).Find(&children)
			for _, child := range children {
				DB.Where("channel_id = ?", child.Id).Delete(&Ability{})
				DB.Delete(&child)
			}
		}
		DB.Where("channel_id IN ?", ids).Delete(&Ability{})
		DB.Where("id IN ?", ids).Delete(&Channel{})
	})
	legacy := Channel{Name: "legacy source", Type: 1, Key: "shared-test-key", BaseURL: &base, Group: "default"}
	require.NoError(t, DB.Create(&legacy).Error)
	ids = append(ids, legacy.Id)
	var children []Channel
	for _, spec := range []struct {
		group string
		video bool
	}{{"source-test-a", false}, {"source-test-a", true}, {"source-test-b", false}} {
		tag := BranchGroupChannelTag(legacy.Id, spec.group, spec.video)
		kind := 1
		if spec.video {
			kind = constant.ChannelTypeTaskPlugin
		}
		setting := `{"task_plugin_key":"aicost-branch"}`
		row := Channel{Name: spec.group, Type: kind, Group: spec.group, Key: legacy.Key, BaseURL: &base, Tag: &tag, Setting: &setting, Models: "kept-model", Status: 1}
		require.NoError(t, DB.Create(&row).Error)
		require.NoError(t, row.AddAbilities(nil))
		children = append(children, row)
		ids = append(ids, row.Id)
	}
	beforePrice := common.OptionMap["ModelPrice"]
	a, err := SaveBranchCatalogGroupKey(0, "source-test-a", "single-a")
	require.NoError(t, err)
	b, err := SaveBranchCatalogGroupKey(0, "source-test-b", "single-b")
	require.NoError(t, err)
	ids = append(ids, a, b)
	require.NotEqual(t, a, b)
	for _, old := range children {
		var row Channel
		require.NoError(t, DB.First(&row, old.Id).Error)
		require.Equal(t, old.Key, row.Key, "another source must not take over a legacy channel")
		require.Equal(t, old.GetTag(), row.GetTag())
		require.Equal(t, old.Models, row.Models)
		require.Equal(t, old.Status, row.Status)
		var abilities int64
		require.NoError(t, DB.Model(&Ability{}).Where("channel_id = ?", old.Id).Count(&abilities).Error)
		require.EqualValues(t, 1, abilities)
	}
	rotated, err := SaveBranchCatalogGroupKey(a, "source-test-a", "rotated-a")
	require.NoError(t, err)
	require.Equal(t, a, rotated)
	_, err = SaveBranchCatalogGroupKey(a, "source-test-b", "wrong-parent")
	require.Error(t, err)
	_, err = SaveBranchCatalogGroupKey(0, "source-test-b", "rotated-a")
	require.Error(t, err)
	var sourceA, sourceB Channel
	require.NoError(t, DB.First(&sourceA, a).Error)
	require.NoError(t, DB.First(&sourceB, b).Error)
	require.Empty(t, sourceA.Models)
	require.Equal(t, "rotated-a", sourceA.Key)
	require.Equal(t, "single-b", sourceB.Key)
	require.NoError(t, DB.First(&legacy, legacy.Id).Error)
	require.Equal(t, "shared-test-key", legacy.Key)
	require.Equal(t, beforePrice, common.OptionMap["ModelPrice"])
	for index, source := range []*Channel{&sourceA, &sourceB} {
		snapshot, err := GetModelPricingSnapshot([]string{"scoped-shared-model"})
		require.NoError(t, err)
		groups, err := GetBranchGroupStates([]string{source.Group})
		require.NoError(t, err)
		item := BranchModelImport{Change: ModelPricingChange{ModelName: "scoped-shared-model", ExpectedVersion: snapshot.Entries[0].Version, Pricing: PricingValues{"ModelPrice": 0.5}}, UpdatePrice: true, Metadata: Model{ModelName: "scoped-shared-model", Endpoints: `["image-generation"]`}, Groups: []string{source.Group}}
		ratio := []float64{0.11, 0.5}[index]
		require.NoError(t, ImportBranchModels(source, []BranchModelImport{item}, BranchGroupSync{Name: source.Group, Ratio: ratio, ExpectedVersion: groups[source.Group].Version, UpdateRatio: true}))
		var child Channel
		require.NoError(t, DB.Where("tag = ?", BranchGroupChannelTag(source.Id, source.Group, false)).First(&child).Error)
		require.Equal(t, source.Key, child.Key)
		require.Equal(t, source.Group, child.Group)
		require.Contains(t, child.GetModels(), "scoped-shared-model")
		require.InDelta(t, ratio, ratio_setting.GetGroupRatio(source.Group), 1e-12)
		routed, err := GetChannel(source.Group, "scoped-shared-model", 0, nil)
		require.NoError(t, err)
		require.NotNil(t, routed)
		require.Equal(t, source.Key, routed.Key, "same model names must not route to another group's credential")
	}
	wrong := BranchModelImport{Groups: []string{"source-test-b"}}
	require.Error(t, ImportBranchModels(&sourceA, []BranchModelImport{wrong}, BranchGroupSync{Name: "source-test-b"}))
}
