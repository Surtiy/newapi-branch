package controller

import (
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestBranchCatalogPreservesCacheAndPricingModes(t *testing.T) {
	zero := 0.0
	write := 1.25
	values, err := branchPricing(branchCatalogItem{Pricing: model.Pricing{ModelName: "branch-language", QuotaType: 0, ModelRatio: 5, CompletionRatio: 3, CacheRatio: &zero, CreateCacheRatio: &write}, ModelType: "language"})
	require.NoError(t, err)
	require.Equal(t, 0.0, values["CacheRatio"])
	require.Equal(t, 1.25, values["CreateCacheRatio"])
	values, err = branchPricing(branchCatalogItem{Pricing: model.Pricing{ModelName: "branch-image", QuotaType: 1, ModelPrice: 0}, ModelType: "image"})
	require.NoError(t, err)
	require.Equal(t, 0.0, values["ModelPrice"])
	require.NotContains(t, values, "ModelRatio")
	_, err = branchPricing(branchCatalogItem{ModelType: "video"})
	require.Error(t, err)
}

func TestVideoBillingExpressionSupportsRequestAndSecond(t *testing.T) {
	request, err := videoBillingExpression("request", 1.25)
	require.NoError(t, err)
	require.Equal(t, `tier("request", 1.25)`, request)

	second, err := videoBillingExpression("second", 0.4)
	require.NoError(t, err)
	require.Equal(t, `tier("second", u("seconds") * 0.4)`, second)

	_, err = videoBillingExpression("token", 1)
	require.Error(t, err)
}

func TestBranchCatalogVersionTracksImportedFieldsOnly(t *testing.T) {
	base := branchCatalog{Version: "upstream-a", Models: []branchCatalogItem{
		{Pricing: model.Pricing{ModelName: "image-a", QuotaType: 1, ModelPrice: 0.3}, ModelType: "image", SyncEnabled: true},
		{Pricing: model.Pricing{ModelName: "image-b", QuotaType: 1, ModelPrice: 0.5}, ModelType: "image", SyncEnabled: true},
	}}
	hash, err := branchCatalogVersion(&base)
	require.NoError(t, err)
	base.Version = "upstream-global-endpoint-map-changed"
	base.GroupRatio = map[string]float64{"default": 2}
	base.Models[0], base.Models[1] = base.Models[1], base.Models[0]
	unchanged, err := branchCatalogVersion(&base)
	require.NoError(t, err)
	require.Equal(t, hash, unchanged, "unimported data and response ordering must not reject a valid preview")
	for _, field := range []string{"price", "billing", "description", "published", "cache", "expression"} {
		t.Run(field, func(t *testing.T) {
			changed := base
			changed.Models = append([]branchCatalogItem(nil), base.Models...)
			item := &changed.Models[0]
			switch field {
			case "price":
				item.ModelPrice = 99
			case "billing":
				item.ModelType = "video"
				item.VideoBillingUnit = "second"
			case "description":
				item.Description = "new description"
			case "published":
				item.SyncEnabled = false
			case "cache":
				item.QuotaType = 0
				ratio := 0.25
				item.CacheRatio = &ratio
			case "expression":
				item.BillingMode = "tiered_expr"
				item.BillingExpr = `tier("request", 0.1)`
			}
			version, err := branchCatalogVersion(&changed)
			require.NoError(t, err)
			require.NotEqual(t, hash, version, "actual imported changes must still require confirmation")
		})
	}
}

func TestBranchCatalogVersionTracksSelectedGroupRules(t *testing.T) {
	base := branchCatalog{GroupRatio: map[string]float64{"group-a": 0.11, "group-b": 1.2}, Models: []branchCatalogItem{
		{Pricing: model.Pricing{ModelName: "image-a", QuotaType: 1, ModelPrice: 0.3, EnableGroup: []string{"group-a"}}, ModelType: "image", SyncEnabled: true},
	}}
	original, err := branchCatalogVersion(&base)
	require.NoError(t, err)
	base.GroupRatio["group-b"] = 2
	unchanged, err := branchCatalogVersion(&base)
	require.NoError(t, err)
	require.Equal(t, original, unchanged)
	base.GroupRatio["group-a"] = 0.5
	changed, err := branchCatalogVersion(&base)
	require.NoError(t, err)
	require.NotEqual(t, original, changed)
	base.GroupRatio["group-a"] = 0.11
	base.Models[0].EnableGroup = []string{"group-b"}
	changed, err = branchCatalogVersion(&base)
	require.NoError(t, err)
	require.NotEqual(t, original, changed)
}
