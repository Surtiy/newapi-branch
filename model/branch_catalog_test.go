package model

import (
	"os"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func setupBranchTestDB(t *testing.T) {
	original := DB
	if dsn := os.Getenv("BRANCH_TEST_POSTGRES"); dsn != "" {
		var err error
		DB, err = gorm.Open(postgres.Open(dsn), &gorm.Config{})
		require.NoError(t, err)
		common.SetDatabaseTypes(common.DatabaseTypePostgreSQL, common.DatabaseTypePostgreSQL)
	}
	if dsn := os.Getenv("BRANCH_TEST_MYSQL"); dsn != "" {
		var err error
		DB, err = gorm.Open(mysql.Open(dsn), &gorm.Config{})
		require.NoError(t, err)
		common.SetDatabaseTypes(common.DatabaseTypeMySQL, common.DatabaseTypeMySQL)
	}
	initCol()
	t.Cleanup(func() {
		DB = original
		common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
		initCol()
	})
	require.NoError(t, DB.AutoMigrate(&Option{}, &Channel{}, &Ability{}, &Model{}, &Vendor{}))
	InitOptionMap()
}

func TestBranchDocsSettingsPersistWithoutChangingPrices(t *testing.T) {
	setupBranchTestDB(t)
	const key = "BranchApiDocsConfig"
	t.Cleanup(func() { DB.Where("key = ?", key).Delete(&Option{}) })
	before := common.OptionMap["ModelPrice"]
	for _, value := range []string{`{"base_url":"https://branch.example.com","docs":[{"id":"video","title":"Video","content":"POST {{BASE_URL}}/v1/videos"}]}`, `{"base_url":"","docs":[]}`} {
		require.NoError(t, UpdateOption(key, value))
		InitOptionMap()
		require.Equal(t, value, common.OptionMap[key])
		require.Equal(t, before, common.OptionMap["ModelPrice"])
	}
}

func TestBranchCatalogImportLifecycle(t *testing.T) {
	setupBranchTestDB(t)
	base := "https://aicost.me"
	channel := Channel{Name: "branch-test", Type: 1, Key: "test-only", Group: "default", Models: "keep-local", Status: 1, BaseURL: &base}
	require.NoError(t, DB.Create(&channel).Error)
	makeImport := func(name string, price float64, update bool) BranchModelImport {
		snapshot, err := GetModelPricingSnapshot([]string{name})
		require.NoError(t, err)
		return BranchModelImport{Change: ModelPricingChange{ModelName: name, ExpectedVersion: snapshot.Entries[0].Version, Pricing: PricingValues{"ModelPrice": price}}, UpdatePrice: update, Metadata: Model{ModelName: name, Description: "main description", Endpoints: `["image-generation"]`}}
	}
	first := makeImport("branch-image-test", 0.5, true)
	require.NoError(t, ImportBranchModels(&channel, []BranchModelImport{first}))
	require.NoError(t, DB.First(&channel, channel.Id).Error)
	require.Contains(t, channel.GetModels(), "keep-local")
	require.Contains(t, channel.GetModels(), "branch-image-test")
	var meta Model
	require.NoError(t, DB.Where("model_name = ?", "branch-image-test").First(&meta).Error)
	require.Equal(t, 0, meta.SyncOfficial)
	custom := makeImport("branch-image-test", 0.8, true)
	require.NoError(t, UpdateModelPricing([]ModelPricingChange{custom.Change}))
	keep := makeImport("branch-image-test", 0.6, false)
	require.NoError(t, ImportBranchModels(&channel, []BranchModelImport{keep}))
	snapshot, err := GetModelPricingSnapshot([]string{"branch-image-test"})
	require.NoError(t, err)
	require.Equal(t, 0.8, snapshot.Entries[0].Configured["ModelPrice"])
	newItem := makeImport("branch-another-test", 0.2, true)
	stale := keep
	stale.Change.ExpectedVersion = "stale"
	require.Error(t, ImportBranchModels(&channel, []BranchModelImport{newItem, stale}))
	var count int64
	require.NoError(t, DB.Model(&Model{}).Where("model_name = ?", "branch-another-test").Count(&count).Error)
	require.Zero(t, count, "conflict must roll back metadata and pricing")
	keep.UpdatePrice = true
	require.NoError(t, ImportBranchModels(&channel, []BranchModelImport{keep}))
	snapshot, err = GetModelPricingSnapshot([]string{"branch-image-test"})
	require.NoError(t, err)
	require.Equal(t, 0.6, snapshot.Entries[0].Configured["ModelPrice"])
}

func TestBranchVideoBillingAndRouting(t *testing.T) {
	require.NoError(t, DB.AutoMigrate(&Option{}, &Channel{}, &Ability{}, &Model{}, &Vendor{}))
	InitOptionMap()
	source, err := os.ReadFile("../branch-plugin.js")
	require.NoError(t, err)
	_, err = jsplugin.DefaultRegistry.Register(string(source), jsplugin.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { jsplugin.DefaultRegistry.Unregister("aicost-branch") })
	for _, test := range []struct {
		expr string
		want float64
	}{{`tier("request", 3.3)`, 3.3}, {`tier("second", u("seconds") * 0.4)`, 12}} {
		err := ValidateBranchPricing("seedance-new", PricingValues{"billing_setting.billing_expr": test.expr}, true)
		require.NoError(t, err)
		cost, _, err := billingexpr.RunExprWithRequest(test.expr, billingexpr.TokenParams{}, billingexpr.RequestInput{Usage: map[string]any{"seconds": 30.0}})
		require.NoError(t, err)
		require.Equal(t, test.want, cost)
	}
	base := "https://aicost.me"
	channel := Channel{Name: "video-import", Type: 1, Key: "test-key", Group: "default", Status: 1, BaseURL: &base}
	require.NoError(t, DB.Create(&channel).Error)
	const name = "seedance-branch-future-model"
	snapshot, err := GetModelPricingSnapshot([]string{name})
	require.NoError(t, err)
	values := PricingValues{"billing_setting.billing_mode": "tiered_expr", "billing_setting.billing_expr": `tier("second", u("seconds") * 0.4)`}
	item := BranchModelImport{Change: ModelPricingChange{ModelName: name, ExpectedVersion: snapshot.Entries[0].Version, Pricing: values}, Video: true, UpdatePrice: true, Metadata: Model{ModelName: name, Endpoints: `["openai-video"]`}}
	require.NoError(t, ImportBranchModels(&channel, []BranchModelImport{item}))
	InitChannelCache()
	target, ok := ResolveTaskModelAlias(jsplugin.DefaultRegistry.Generation(), name)
	require.True(t, ok, "future model names must resolve without rebuilding the plugin")
	require.Equal(t, "aicost-branch", target.PluginKey)
	require.NoError(t, ValidateModelPricing(name, values), "ordinary local price editing must recognize the imported video")
}
