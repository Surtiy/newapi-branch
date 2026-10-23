package controller

import (
	"context"
	"errors"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

type branchCatalogTransport func(*http.Request) (*http.Response, error)

func (f branchCatalogTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func branchCatalogModelsResponse(t *testing.T, r *http.Request, names ...string) *http.Response {
	t.Helper()
	data := make([]map[string]string, 0, len(names))
	for _, name := range names {
		data = append(data, map[string]string{"id": name})
	}
	body, err := common.Marshal(map[string]any{"object": "list", "data": data})
	require.NoError(t, err)
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body))), Request: r}
}

func TestBranchCatalogMaskedKeyPreview(t *testing.T) {
	for _, test := range []struct{ key, preview string }{
		{"", ""},
		{"  ", ""},
		{"sk-Abcd-secret-middle-Wxyz", "sk-Abcd**********Wxyz"},
		{"  sk-Abcd-secret-middle-Wxyz  ", "sk-Abcd**********Wxyz"},
		{"Abcd-secret-middle-Wxyz", "Abcd**********Wxyz"},
		{"sk-Abcd", "sk-****"},
		{"sk-Abcdefgh", "sk-Ab****gh"},
		{"sk-Abcdefghi", "sk-Abcd**********fghi"},
	} {
		require.Equal(t, test.preview, branchCatalogKeyPreview(test.key))
	}
}

func TestBranchCatalogDeleteAuthorizationAndVersionGuard(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Option{}, &model.Log{}, &model.AuditLog{}))
	id, err := model.SaveBranchCatalogGroupsKey(0, []string{"delete-controller"}, "sk-delete-private-key")
	require.NoError(t, err)
	var source model.Channel
	require.NoError(t, db.First(&source, id).Error)
	call := func(body string, role int) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set("id", 1)
		c.Set("role", role)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/option/branch_catalog/delete", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		DeleteBranchCatalogSource(c)
		return w
	}
	body := fmt.Sprintf(`{"channel_id":%d,"channel_version":"%s"}`, id, model.BranchCatalogSourceVersion(&source))
	w := call(body, common.RoleCommonUser)
	require.Equal(t, http.StatusForbidden, w.Code)
	for _, invalid := range []string{`{}`, fmt.Sprintf(`{"channel_id":%d,"channel_version":"%s"}`, id, strings.Repeat("0", 64))} {
		w = call(invalid, common.RoleRootUser)
		require.Contains(t, w.Body.String(), `"success":false`)
		require.NoError(t, db.First(&source, id).Error)
	}
	w = call(body, common.RoleRootUser)
	require.Contains(t, w.Body.String(), `"success":true`)
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	require.NotContains(t, w.Body.String(), source.Key)
	w = call(body, common.RoleRootUser)
	require.Contains(t, w.Body.String(), `"success":false`)
	var audits []model.AuditLog
	require.NoError(t, db.Find(&audits).Error)
	raw, err := common.Marshal(audits)
	require.NoError(t, err)
	require.NotContains(t, string(raw), source.Key)
}

func TestBranchCatalogSaveAllowsSharedDirectoryGroupsWithoutOverwritingOwners(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Option{}, &model.Log{}, &model.AuditLog{}))
	first, err := model.SaveBranchCatalogGroupsKey(0, []string{"a", "b"}, "sk-aaaa-private-first-1111")
	require.NoError(t, err)
	second, err := model.SaveBranchCatalogGroupsKey(0, []string{"c"}, "sk-bbbb-private-second-2222")
	require.NoError(t, err)
	originalTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = originalTransport })
	http.DefaultTransport = branchCatalogTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/v1/models" {
			return branchCatalogModelsResponse(t, r, "image"), nil
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"success":true,"data":{"catalog_version":"v1","models":[{"model_name":"image","sync_enabled":true,"group_ratios":{"a":1,"b":1,"c":1}}]}}`)), Request: r}, nil
	})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("id", 1)
	c.Set("role", common.RoleRootUser)
	c.Request = httptest.NewRequest(http.MethodPost, "/config", strings.NewReader(`{"api_key":"sk-cccc-private-incoming-3333"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	ConfigureBranchCatalog(c)
	body := w.Body.String()
	require.Contains(t, body, `"success":true`)
	require.Contains(t, body, `"conflicts":[]`)
	require.NotContains(t, body, "private")
	var count int64
	require.NoError(t, db.Model(&model.Channel{}).Count(&count).Error)
	require.EqualValues(t, 3, count)
	for id, key := range map[int]string{first: "sk-aaaa-private-first-1111", second: "sk-bbbb-private-second-2222"} {
		var owner model.Channel
		require.NoError(t, db.First(&owner, id).Error)
		require.Equal(t, key, owner.Key)
	}
	message := branchCatalogSaveErrorMessage("sk-incoming-secret-key", errors.New("database password secret"))
	require.NotContains(t, message, "secret")
	message = branchCatalogSaveErrorMessage("sk-aaaa-private-first-1111", &model.BranchCatalogKeyConflictError{Conflicts: []model.BranchCatalogKeyConflict{{ChannelID: first, Groups: []string{"a", "b"}, DuplicateKey: true}}})
	require.Contains(t, message, "该令牌已保存")
	require.Contains(t, message, "sk-aaaa**********1111")
	require.NotContains(t, message, "private")
}

func TestBranchCatalogKeyConfiguration(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Option{}, &model.Log{}, &model.AuditLog{}))
	originalTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = originalTransport })
	http.DefaultTransport = branchCatalogTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/v1/models" {
			return branchCatalogModelsResponse(t, r, "image"), nil
		}
		require.Equal(t, "https://aicost.me/api/distributor/models", r.URL.String())
		status := http.StatusOK
		if r.Header.Get("Authorization") != "Bearer test-valid-key" {
			status = http.StatusUnauthorized
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"success":true,"data":{"catalog_version":"v1","models":[{"model_name":"image","sync_enabled":true,"group_ratios":{"image-group":1}}]}}`)), Request: r}, nil
	})
	call := func(body string, role int) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set("id", 1)
		c.Set("role", role)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/option/branch_catalog/config", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		ConfigureBranchCatalog(c)
		return w
	}
	w := call(`{"api_key":"test-valid-key","group":"image-group"}`, common.RoleRootUser)
	require.Contains(t, w.Body.String(), `"success":true`)
	require.NotContains(t, w.Body.String(), "test-valid-key")
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	var parent model.Channel
	require.NoError(t, db.First(&parent).Error)
	require.Equal(t, "test-valid-key", parent.Key)
	require.Equal(t, "image-group", model.BranchCatalogBoundGroup(&parent))
	oldVersion := branchChannelVersion(&parent)
	w = call(fmt.Sprintf(`{"channel_id":%d,"api_key":"test-valid-key"}`, parent.Id), common.RoleRootUser)
	require.Contains(t, w.Body.String(), `"success":true`)
	require.Contains(t, w.Body.String(), `"groups":["image-group"]`)
	require.NotContains(t, w.Body.String(), "test-valid-key")
	require.NoError(t, db.First(&parent).Error)
	require.Equal(t, oldVersion, branchChannelVersion(&parent))
	for _, body := range []string{`{"api_key":"test-rejected-key","group":"image-group"}`, `{"api_key":"Bearer test-valid-key"}`, `{"api_key":""}`, `{"api_key":"test-valid-key","group":"unknown"}`} {
		w = call(body, common.RoleRootUser)
		require.Contains(t, w.Body.String(), `"success":false`)
		require.NoError(t, db.First(&parent).Error)
		require.Equal(t, "test-valid-key", parent.Key)
	}
	w = call(`{"api_key":"test-valid-key"}`, common.RoleCommonUser)
	require.Equal(t, http.StatusForbidden, w.Code)
	w = httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("role", common.RoleRootUser)
	GetBranchCatalogChannels(c)
	require.Contains(t, w.Body.String(), `"configured":true`)
	require.Contains(t, w.Body.String(), `"key_preview":"test**********-key"`)
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	require.NotContains(t, w.Body.String(), "test-valid-key")
	parent.Key = "replacement"
	require.NotEqual(t, oldVersion, branchChannelVersion(&parent))
}

func TestBranchCatalogScopesAccountDirectoryToBoundGroup(t *testing.T) {
	catalog := branchCatalog{Models: []branchCatalogItem{
		{Pricing: model.Pricing{ModelName: "shared", EnableGroup: []string{"a", "b"}}, GroupRatios: map[string]float64{"a": 0.5, "b": 2}},
		{Pricing: model.Pricing{ModelName: "b-only"}, GroupRatios: map[string]float64{"b": 2}},
	}}
	require.NoError(t, scopeBranchCatalogGroup(&catalog, "a"))
	require.Equal(t, map[string]float64{"a": 0.5}, catalog.GroupRatio)
	require.Len(t, catalog.Models, 1)
	require.Equal(t, []string{"a"}, catalog.Models[0].EnableGroup)
	require.Equal(t, map[string]float64{"a": 0.5}, catalog.Models[0].GroupRatios)
	for _, group := range []string{"", "auto", " a", "a,b", "unknown"} {
		require.Error(t, scopeBranchCatalogGroup(&catalog, group))
	}
}

func TestBranchCatalogUsesUpstreamDirectoryWithoutLocalApproval(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Option{}, &model.Log{}, &model.AuditLog{}))
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = branchCatalogTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/v1/models" {
			return branchCatalogModelsResponse(t, r, "image", "video", "shared"), nil
		}
		require.Equal(t, "https://aicost.me/api/distributor/models", r.URL.String())
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"success":true,"data":{"catalog_version":"v1","models":[{"model_name":"image","sync_enabled":true,"group_ratios":{"images":1}},{"model_name":"video","sync_enabled":true,"group_ratios":{"videos":2}},{"model_name":"shared","sync_enabled":true,"group_ratios":{"images":1,"videos":2}}]}}`)), Request: r}, nil
	})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("id", 1)
	c.Set("role", common.RoleRootUser)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/option/branch_catalog/config", strings.NewReader(`{"api_key":"directory-key"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	ConfigureBranchCatalog(c)
	require.Contains(t, w.Body.String(), `"success":true`)
	require.Contains(t, w.Body.String(), `"groups":["images","videos"]`)
	require.Contains(t, w.Body.String(), `"conflicts":[]`)
	require.NotContains(t, w.Body.String(), "directory-key")
	var source model.Channel
	require.NoError(t, db.First(&source).Error)
	require.Equal(t, []string{"images", "videos"}, model.BranchCatalogBoundGroups(&source))

	// A replacement follows the current API directory, not a local key registry.
	first, err := model.SaveBranchCatalogGroupsKey(0, []string{"single"}, "single-key")
	require.NoError(t, err)
	http.DefaultTransport = branchCatalogTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/v1/models" {
			return branchCatalogModelsResponse(t, r, "single-model"), nil
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"success":true,"data":{"catalog_version":"v2","models":[{"model_name":"single-model","sync_enabled":true,"group_ratios":{"single":1}}]}}`)), Request: r}, nil
	})
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Set("id", 1)
	c.Set("role", common.RoleRootUser)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/option/branch_catalog/config", strings.NewReader(fmt.Sprintf(`{"channel_id":%d,"api_key":"replacement"}`, first)))
	c.Request.Header.Set("Content-Type", "application/json")
	ConfigureBranchCatalog(c)
	require.Contains(t, w.Body.String(), `"success":true`)
	source = model.Channel{}
	require.NoError(t, db.First(&source, first).Error)
	require.Equal(t, []string{"single"}, model.BranchCatalogBoundGroups(&source))
	require.Equal(t, "replacement", source.Key)
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/preview", strings.NewReader(fmt.Sprintf(`{"channel_id":%d}`, first)))
	c.Request.Header.Set("Content-Type", "application/json")
	PreviewBranchCatalog(c)
	require.Contains(t, w.Body.String(), `"success":true`)
	require.NotContains(t, w.Body.String(), "outside")
	require.NoError(t, db.First(&source, first).Error)
	require.Equal(t, []string{"single"}, model.BranchCatalogBoundGroups(&source))
}

func TestBranchMultiGroupCatalogIsAdvisoryAndCanBeImported(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Option{}, &model.Log{}, &model.AuditLog{}))
	model.InitOptionMap()
	parentID, err := model.SaveBranchCatalogGroupsKey(0, []string{"a", "b"}, "test-multi-key")
	require.NoError(t, err)
	var parent model.Channel
	require.NoError(t, db.First(&parent, parentID).Error)
	catalog := branchCatalog{Version: "v1", Models: []branchCatalogItem{
		{Pricing: model.Pricing{ModelName: "shared", QuotaType: 1, ModelPrice: 0.5, SupportedEndpointTypes: []constant.EndpointType{constant.EndpointTypeImageGeneration}}, ModelType: "image", SyncEnabled: true, GroupRatios: map[string]float64{"a": 1, "b": 2}},
		{Pricing: model.Pricing{ModelName: "unique", QuotaType: 1, ModelPrice: 0.4}, ModelType: "image", SyncEnabled: true, GroupRatios: map[string]float64{"b": 2}},
	}}
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = branchCatalogTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/v1/models" {
			return branchCatalogModelsResponse(t, r, "shared", "unique"), nil
		}
		body, err := common.Marshal(map[string]any{"success": true, "data": catalog})
		require.NoError(t, err)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body))), Request: r}, nil
	})
	configResponse := httptest.NewRecorder()
	configContext, _ := gin.CreateTestContext(configResponse)
	configContext.Set("id", 1)
	configContext.Set("role", common.RoleRootUser)
	configContext.Request = httptest.NewRequest(http.MethodPost, "/config", strings.NewReader(`{"api_key":"test-multi-key","groups":["b","a"]}`))
	configContext.Request.Header.Set("Content-Type", "application/json")
	ConfigureBranchCatalog(configContext)
	require.Contains(t, configResponse.Body.String(), `"success":true`)
	require.Contains(t, configResponse.Body.String(), `"groups":["a","b"]`)
	require.Contains(t, configResponse.Body.String(), `"conflicts":[]`)
	require.NotContains(t, configResponse.Body.String(), "test-multi-key")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/preview", strings.NewReader(fmt.Sprintf(`{"channel_id":%d}`, parentID)))
	c.Request.Header.Set("Content-Type", "application/json")
	PreviewBranchCatalog(c)
	require.NotContains(t, w.Body.String(), `"conflicting_groups"`)
	require.NotContains(t, w.Body.String(), `"blocked"`)
	require.NotContains(t, w.Body.String(), "test-multi-key")
	require.NoError(t, scopeBranchCatalog(&catalog))
	filterBranchCatalogGroups(&catalog, []string{"a", "b"})
	require.Empty(t, branchConflictGroups(catalog.Models[0]))
	require.Empty(t, branchConflictGroups(catalog.Models[1]))
	version, err := branchCatalogVersion(&catalog)
	require.NoError(t, err)
	require.NoError(t, db.First(&parent, parentID).Error)
	snapshot, err := model.GetModelPricingSnapshot([]string{"shared"})
	require.NoError(t, err)
	groups, err := model.GetBranchGroupStates([]string{"a"})
	require.NoError(t, err)
	body := fmt.Sprintf(`{"channel_id":%d,"catalog_version":%q,"channel_version":%q,"price_multiplier":1,"models":[{"model_name":"shared","expected_version":%q,"groups":["a"]}],"groups":[{"name":"a","expected_version":%q,"update_ratio":true}]}`, parentID, version, branchChannelVersion(&parent), snapshot.Entries[0].Version, groups["a"].Version)
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/apply", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	ApplyBranchCatalog(c)
	require.Contains(t, w.Body.String(), `"success":true`, w.Body.String())
	snapshot, err = model.GetModelPricingSnapshot([]string{"shared"})
	require.NoError(t, err)
	require.Equal(t, 0.5, snapshot.Entries[0].Configured["ModelPrice"])
}

func TestBranchCatalogConflictsAcrossSavedTokens(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Option{}))
	a, err := model.SaveBranchCatalogGroupKey(0, "a", "sk-aaaa-private-1111")
	require.NoError(t, err)
	b, err := model.SaveBranchCatalogGroupKey(0, "b", "sk-bbbb-private-2222")
	require.NoError(t, err)
	var source model.Channel
	require.NoError(t, db.First(&source, b).Error)
	_, err = model.ReconcileBranchCatalogSource(&source, []model.BranchCatalogModelScope{{ModelName: "shared", Groups: []string{"b"}}})
	require.NoError(t, err)
	catalog := branchCatalog{Models: []branchCatalogItem{
		{Pricing: model.Pricing{ModelName: "shared"}, SyncEnabled: true, GroupRatios: map[string]float64{"a": 1}},
		{Pricing: model.Pricing{ModelName: "unique"}, SyncEnabled: true, GroupRatios: map[string]float64{"a": 1, "b": 2}},
	}}
	version, err := branchCatalogVersion(&catalog)
	require.NoError(t, err)
	require.NoError(t, markBranchCatalogConflicts(&catalog, a))
	require.Empty(t, branchConflictGroups(catalog.Models[0]))
	require.Equal(t, map[string]float64{"a": 1}, catalog.Models[0].GroupRatios)
	require.Empty(t, catalog.Models[0].DuplicateSources)
	require.Empty(t, catalog.Models[1].DuplicateSources)
	markedVersion, err := branchCatalogVersion(&catalog)
	require.NoError(t, err)
	require.Equal(t, version, markedVersion, "advisory warnings must not stale an import batch")
	catalog.Models[0].GroupRatios["b"] = 2
	require.NoError(t, markBranchCatalogConflicts(&catalog, a))
	require.Equal(t, []string{"b"}, branchConflictGroups(catalog.Models[0]))
	require.Equal(t, []branchDuplicateSource{{ChannelID: b, KeyPreview: "sk-bbbb**********2222", Groups: []string{"b"}}}, catalog.Models[0].DuplicateSources)
	_, err = model.ReconcileBranchCatalogSource(&source, []model.BranchCatalogModelScope{{ModelName: "renamed", Groups: []string{"b"}}})
	require.NoError(t, err)
	require.NoError(t, markBranchCatalogConflicts(&catalog, a))
	require.Empty(t, catalog.Models[0].DuplicateSources)
	require.Empty(t, catalog.Models[0].ConflictingGroups)
}

func TestBranchCatalogApplyExplicitOneUpdatesOldPrice(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Option{}, &model.Log{}, &model.AuditLog{}))
	model.InitOptionMap()
	require.NoError(t, model.UpdateOption("GroupRatio", `{"image-group":1}`))
	parentID, err := model.SaveBranchCatalogGroupKey(0, "image-group", "test-key")
	require.NoError(t, err)
	var parent model.Channel
	require.NoError(t, db.First(&parent, parentID).Error)
	item := branchCatalogItem{Pricing: model.Pricing{ModelName: "branch-explicit-one", QuotaType: 1, ModelPrice: 0.5, SupportedEndpointTypes: []constant.EndpointType{constant.EndpointTypeImageGeneration}}, ModelType: "image", SyncEnabled: true, GroupRatios: map[string]float64{"image-group": 1}}
	catalog := branchCatalog{Version: "catalog", Models: []branchCatalogItem{item}, GroupRatio: item.GroupRatios}
	_, err = model.ReconcileBranchCatalogSource(&parent, branchCatalogScopes(&catalog))
	require.NoError(t, err)
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = branchCatalogTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/v1/models" {
			return branchCatalogModelsResponse(t, r, item.ModelName), nil
		}
		body, err := common.Marshal(map[string]any{"success": true, "data": catalog})
		require.NoError(t, err)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body))), Request: r}, nil
	})
	for _, explicit := range []bool{false, true} {
		snapshot, err := model.GetModelPricingSnapshot([]string{item.ModelName})
		require.NoError(t, err)
		require.NoError(t, model.UpdateModelPricing([]model.ModelPricingChange{{ModelName: item.ModelName, ExpectedVersion: snapshot.Entries[0].Version, Pricing: model.PricingValues{"ModelPrice": 0.6, "billing_setting.billing_mode": "ratio"}}}))
		require.NoError(t, db.First(&parent, parentID).Error)
		snapshot, err = model.GetModelPricingSnapshot([]string{item.ModelName})
		require.NoError(t, err)
		groups, err := model.GetBranchGroupStates([]string{"image-group"})
		require.NoError(t, err)
		version, err := branchCatalogVersion(&catalog)
		require.NoError(t, err)
		payload := map[string]any{"channel_id": parentID, "catalog_version": version, "channel_version": branchChannelVersion(&parent), "models": []any{map[string]any{"model_name": item.ModelName, "expected_version": snapshot.Entries[0].Version, "update_price": false, "groups": []string{"image-group"}}}, "groups": []model.BranchGroupSync{{Name: "image-group", ExpectedVersion: groups["image-group"].Version}}}
		if explicit {
			payload["price_multiplier"] = 1
		}
		raw, err := common.Marshal(payload)
		require.NoError(t, err)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set("role", common.RoleRootUser)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/option/branch_catalog/apply", strings.NewReader(string(raw)))
		c.Request.Header.Set("Content-Type", "application/json")
		ApplyBranchCatalog(c)
		require.Contains(t, w.Body.String(), `"success":true`, w.Body.String())
		snapshot, err = model.GetModelPricingSnapshot([]string{item.ModelName})
		require.NoError(t, err)
		expected := 0.6
		if explicit {
			expected = 0.5
		}
		require.Equal(t, expected, snapshot.Entries[0].Configured["ModelPrice"])
		if explicit {
			require.Contains(t, w.Body.String(), snapshot.Entries[0].Version, "committed version lets another group import the same model without using the stale pre-batch version")
		}
	}
}

func TestBranchCatalogSameModelSyncsDistinctGroupsWithCommittedVersions(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Option{}, &model.Log{}, &model.AuditLog{}))
	model.InitOptionMap()
	ids := []int{}
	for _, group := range []string{"group-a", "group-b"} {
		id, err := model.SaveBranchCatalogGroupKey(0, group, group+"-key")
		require.NoError(t, err)
		ids = append(ids, id)
	}
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = branchCatalogTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/v1/models" {
			return branchCatalogModelsResponse(t, r, "shared-group-model"), nil
		}
		group := strings.TrimSuffix(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), "-key")
		ratio := map[string]float64{"group-a": 0.11, "group-b": 0.5}[group]
		body := fmt.Sprintf(`{"success":true,"data":{"catalog_version":"same","models":[{"model_name":"shared-group-model","model_type":"image","sync_enabled":true,"quota_type":1,"model_price":0.5,"supported_endpoint_types":["image-generation"],"group_ratios":{"%s":%g}}]}}`, group, ratio)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	call := func(handler func(*gin.Context), body any) *httptest.ResponseRecorder {
		encoded, err := common.Marshal(body)
		require.NoError(t, err)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set("id", 1)
		c.Set("role", common.RoleRootUser)
		c.Request = httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(string(encoded)))
		c.Request.Header.Set("Content-Type", "application/json")
		handler(c)
		return w
	}
	var firstVersion string
	for index, id := range ids {
		preview := call(PreviewBranchCatalog, map[string]any{"channel_id": id})
		var envelope struct {
			Success bool `json:"success"`
			Data    struct {
				CatalogVersion string             `json:"catalog_version"`
				ChannelVersion string             `json:"channel_version"`
				Models         []branchPreviewRow `json:"models"`
			} `json:"data"`
		}
		require.NoError(t, common.UnmarshalJsonStr(preview.Body.String(), &envelope))
		require.True(t, envelope.Success, preview.Body.String())
		require.Len(t, envelope.Data.Models, 1)
		row := envelope.Data.Models[0]
		require.Empty(t, row.DuplicateSources)
		if index == 1 {
			require.Equal(t, firstVersion, row.Local.Version)
		}
		selection := map[string]any{"model_name": row.ModelName, "expected_version": row.Local.Version, "groups": []string{row.Groups[0].Name}}
		payload := map[string]any{"channel_id": id, "catalog_version": envelope.Data.CatalogVersion, "channel_version": envelope.Data.ChannelVersion, "price_multiplier": 1, "models": []any{selection}, "groups": []any{map[string]any{"name": row.Groups[0].Name, "expected_version": row.Groups[0].Local.Version, "update_ratio": true}}}
		applied := call(ApplyBranchCatalog, payload)
		var result struct {
			Success bool `json:"success"`
			Data    struct {
				Versions map[string]string `json:"pricing_versions"`
			} `json:"data"`
		}
		require.NoError(t, common.UnmarshalJsonStr(applied.Body.String(), &result))
		require.True(t, result.Success, applied.Body.String())
		firstVersion = result.Data.Versions[row.ModelName]
		require.Len(t, firstVersion, 64)
		var child model.Channel
		require.NoError(t, db.Where("tag = ?", model.BranchGroupChannelTag(id, row.Groups[0].Name, false)).First(&child).Error)
		require.Equal(t, row.Groups[0].Name+"-key", child.Key)
		require.Equal(t, []string{row.ModelName}, child.GetModels())
		states, err := model.GetBranchGroupStates([]string{child.Group})
		require.NoError(t, err)
		require.InDelta(t, row.Groups[0].Ratio, *states[child.Group].Ratio, 1e-12)
	}
}

func TestBranchCatalogDoesNotFollowRedirectsOrExposeUpstreamErrors(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = branchCatalogTransport(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, "aicost.me", r.URL.Host)
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {"https://other.example/catalog"}}, Body: io.NopCloser(strings.NewReader("upstream-secret")), Request: r}, nil
	})
	_, err := requestBranchCatalog(context.Background(), "test-secret")
	require.ErrorContains(t, err, "302")
	require.NotContains(t, err.Error(), "secret")
}

func TestRequestBranchCatalogKeepsOnlyAPIKeyModelsAndGroups(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = branchCatalogTransport(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		if r.URL.Path == "/v1/models" {
			return branchCatalogModelsResponse(t, r, "allowed", "outside-key", "not-published"), nil
		}
		body := `{"success":true,"data":{"catalog_version":"v1","group_ratio":{"key-group":0.8,"other-group":9},"models":[{"model_name":"allowed","sync_enabled":true,"group_ratios":{"key-group":0.8}},{"model_name":"outside-key","sync_enabled":true},{"model_name":"not-published","sync_enabled":false,"group_ratios":{"key-group":0.8}}]}}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})

	catalog, err := requestBranchCatalog(context.Background(), "test-key")
	require.NoError(t, err)
	require.Equal(t, map[string]float64{"key-group": 0.8}, catalog.GroupRatio)
	require.Len(t, catalog.Models, 1)
	require.Equal(t, "allowed", catalog.Models[0].ModelName)
}

func TestBranchCatalogRemovedTokenModelDoesNotConflictWithSavedGroup(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Option{}, &model.Log{}, &model.AuditLog{}))
	ownerID, err := model.SaveBranchCatalogGroupKey(0, "image-group", "sk-owner-private-1111")
	require.NoError(t, err)
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = branchCatalogTransport(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, "Bearer sk-new-private-2222", r.Header.Get("Authorization"))
		if r.URL.Path == "/v1/models" {
			return branchCatalogModelsResponse(t, r, "video"), nil
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"success":true,"data":{"catalog_version":"v1","models":[{"model_name":"image","sync_enabled":true,"group_ratios":{"image-group":1}},{"model_name":"video","sync_enabled":true,"group_ratios":{"video-group":1}}]}}`)), Request: r}, nil
	})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("id", 1)
	c.Set("role", common.RoleRootUser)
	c.Request = httptest.NewRequest(http.MethodPost, "/config", strings.NewReader(`{"api_key":"sk-new-private-2222"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	ConfigureBranchCatalog(c)
	require.Contains(t, w.Body.String(), `"success":true`)
	require.Contains(t, w.Body.String(), `"groups":["video-group"]`)
	require.NotContains(t, w.Body.String(), "image-group")
	require.NotContains(t, w.Body.String(), "private")
	var owner, source model.Channel
	require.NoError(t, db.First(&owner, ownerID).Error)
	require.Equal(t, "sk-owner-private-1111", owner.Key)
	require.NoError(t, db.Where("id <> ?", ownerID).First(&source).Error)
	require.Equal(t, []string{"video-group"}, model.BranchCatalogBoundGroups(&source))
}

func TestBranchCatalogTokenModelsFailureNeverUsesAccountDirectory(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
	}{
		{"denied", http.StatusUnauthorized, "secret-upstream-error"},
		{"unavailable", http.StatusServiceUnavailable, "secret-upstream-error"},
		{"redirect", http.StatusFound, "secret-upstream-error"},
		{"invalid", http.StatusOK, `{"data":[{"id":"image"}]}`},
		{"missing", http.StatusOK, `{"object":"list"}`},
		{"null", http.StatusOK, `{"object":"list","data":null}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			original := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = original })
			http.DefaultTransport = branchCatalogTransport(func(r *http.Request) (*http.Response, error) {
				require.Equal(t, "aicost.me", r.URL.Host)
				body, status := `{"success":true,"data":{"catalog_version":"v1","models":[{"model_name":"image","sync_enabled":true,"group_ratios":{"images":1}}]}}`, http.StatusOK
				if r.URL.Path == "/v1/models" {
					body, status = test.body, test.status
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Location": {"https://other.example/"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})
			catalog, err := requestBranchCatalog(context.Background(), "secret-key")
			require.Error(t, err)
			require.Nil(t, catalog)
			require.NotContains(t, err.Error(), "secret")
		})
	}
}

func TestBranchCatalogRefreshPrunesOnlyAfterAValidCompleteSnapshot(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Option{}))
	id, err := model.SaveBranchCatalogGroupKey(0, "refresh", "test-refresh-key")
	require.NoError(t, err)
	var source model.Channel
	require.NoError(t, db.First(&source, id).Error)
	tag := model.BranchGroupChannelTag(id, "refresh", false)
	child := model.Channel{Type: 1, Key: source.Key, BaseURL: source.BaseURL, Group: "refresh", Tag: &tag, Models: "kept,removed", Status: common.ChannelStatusEnabled}
	require.NoError(t, db.Create(&child).Error)
	require.NoError(t, child.AddAbilities(nil))
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	status := http.StatusOK
	names := []string{"kept", "added"}
	http.DefaultTransport = branchCatalogTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/v1/models" {
			response := branchCatalogModelsResponse(t, r, names...)
			response.StatusCode = status
			return response, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"success":true,"data":{"catalog_version":"refresh","models":[{"model_name":"kept","sync_enabled":true,"group_ratios":{"refresh":1}},{"model_name":"added","sync_enabled":true,"group_ratios":{"refresh":1}}]}}`)), Request: r}, nil
	})
	for _, step := range []struct {
		status  int
		models  []string
		want    string
		success bool
	}{
		{http.StatusOK, []string{"kept", "added"}, "kept", true},
		{http.StatusServiceUnavailable, nil, "kept", false},
		{http.StatusOK, nil, "kept", true},
	} {
		status, names = step.status, step.models
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/preview", strings.NewReader(fmt.Sprintf(`{"channel_id":%d}`, id)))
		c.Request.Header.Set("Content-Type", "application/json")
		PreviewBranchCatalog(c)
		require.Contains(t, w.Body.String(), fmt.Sprintf(`"success":%t`, step.success), w.Body.String())
		require.NoError(t, db.First(&child, child.Id).Error)
		require.Equal(t, step.want, child.Models)
		if step.success && len(names) == 0 {
			require.Equal(t, common.ChannelStatusManuallyDisabled, child.Status)
			require.Contains(t, w.Body.String(), `"models":[]`)
		}
	}
}

func TestBranchCatalogPreservesCacheAndPricingModes(t *testing.T) {
	zero := 0.0
	write := 1.25
	values, err := branchPricing(branchCatalogItem{Pricing: model.Pricing{ModelName: "branch-language", QuotaType: 0, ModelRatio: 5, CompletionRatio: 3, CacheRatio: &zero, CreateCacheRatio: &write}, ModelType: "language"}, 1)
	require.NoError(t, err)
	require.Equal(t, 0.0, values["CacheRatio"])
	require.Equal(t, 1.25, values["CreateCacheRatio"])
	values, err = branchPricing(branchCatalogItem{Pricing: model.Pricing{ModelName: "branch-image", QuotaType: 1, ModelPrice: 0}, ModelType: "image"}, 1)
	require.NoError(t, err)
	require.Equal(t, 0.0, values["ModelPrice"])
	require.NotContains(t, values, "ModelRatio")
	_, err = branchPricing(branchCatalogItem{ModelType: "video"}, 1)
	require.Error(t, err)
}

func TestScopeBranchCatalogUsesAPIKeyGroupsOnly(t *testing.T) {
	catalog := branchCatalog{
		GroupRatio: map[string]float64{"key-group": 0.8, "other-group": 9},
		Models: []branchCatalogItem{
			{Pricing: model.Pricing{ModelName: "allowed", EnableGroup: []string{"key-group", "other-group"}}, SyncEnabled: true, GroupRatios: map[string]float64{"key-group": 0.8}},
			{Pricing: model.Pricing{ModelName: "outside-key", EnableGroup: []string{"other-group"}}, SyncEnabled: true},
			{Pricing: model.Pricing{ModelName: "not-published"}, SyncEnabled: false, GroupRatios: map[string]float64{"key-group": 0.8}},
		},
	}
	require.NoError(t, scopeBranchCatalog(&catalog))
	require.Len(t, catalog.Models, 1)
	require.Equal(t, "allowed", catalog.Models[0].ModelName)
	require.Equal(t, map[string]float64{"key-group": 0.8}, catalog.GroupRatio)
	require.Equal(t, map[string]float64{"key-group": 0.8}, branchItemGroups(catalog.Models[0]))
}

func TestScopeBranchCatalogRejectsInconsistentAPIKeyGroups(t *testing.T) {
	catalog := branchCatalog{Models: []branchCatalogItem{
		{Pricing: model.Pricing{ModelName: "first"}, SyncEnabled: true, GroupRatios: map[string]float64{"key-group": 0.8}},
		{Pricing: model.Pricing{ModelName: "second"}, SyncEnabled: true, GroupRatios: map[string]float64{"key-group": 1.2}},
	}}
	require.ErrorContains(t, scopeBranchCatalog(&catalog), "倍率不一致")
}

func TestBranchPricingAppliesUniformPriceMultiplier(t *testing.T) {
	source, err := os.ReadFile("../branch-plugin.js")
	require.NoError(t, err)
	_, err = jsplugin.DefaultRegistry.Register(string(source), jsplugin.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { jsplugin.DefaultRegistry.Unregister("aicost-branch") })
	language, err := branchPricing(branchCatalogItem{Pricing: model.Pricing{ModelName: "language", QuotaType: 0, ModelRatio: 2, CompletionRatio: 4}, ModelType: "language"}, 1.5)
	require.NoError(t, err)
	require.Equal(t, 3.0, language["ModelRatio"])
	require.Equal(t, 4.0, language["CompletionRatio"], "relative output multiplier must not be applied twice")

	image, err := branchPricing(branchCatalogItem{Pricing: model.Pricing{ModelName: "image", QuotaType: 1, ModelPrice: 0.2}, ModelType: "image"}, 1.5)
	require.NoError(t, err)
	require.InDelta(t, 0.3, image["ModelPrice"], 1e-12)

	video, err := branchPricing(branchCatalogItem{Pricing: model.Pricing{ModelName: "video", ModelPrice: 0.4}, ModelType: "video", VideoBillingUnit: "second"}, 1.5)
	require.NoError(t, err)
	require.Equal(t, `tier("second", u("seconds") * 0.6)`, video["billing_setting.billing_expr"])

	expression, err := branchPricing(branchCatalogItem{Pricing: model.Pricing{ModelName: "expression", BillingMode: "tiered_expr", BillingExpr: `tier("base", p * 2 + c * 8)`}, ModelType: "language"}, 1.5)
	require.NoError(t, err)
	require.Equal(t, `(tier("base", p * 2 + c * 8)) * 1.5`, expression["billing_setting.billing_expr"])
	versioned, err := branchPricing(branchCatalogItem{Pricing: model.Pricing{ModelName: "versioned", BillingMode: "tiered_expr", BillingExpr: `v1:tier("base", p * 2)`}, ModelType: "language"}, 1.5)
	require.NoError(t, err)
	require.Equal(t, `v1:(tier("base", p * 2)) * 1.5`, versioned["billing_setting.billing_expr"])

	for _, multiplier := range []float64{0, -1, math.Inf(1), math.NaN(), 1001} {
		_, err := branchPricing(branchCatalogItem{Pricing: model.Pricing{ModelName: "image", QuotaType: 1, ModelPrice: 1}, ModelType: "image"}, multiplier)
		require.Error(t, err)
	}
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
		{Pricing: model.Pricing{ModelName: "image-a", QuotaType: 1, ModelPrice: 0.3}, ModelType: "image", SyncEnabled: true, GroupRatios: map[string]float64{"default": 1}},
		{Pricing: model.Pricing{ModelName: "image-b", QuotaType: 1, ModelPrice: 0.5}, ModelType: "image", SyncEnabled: true, GroupRatios: map[string]float64{"default": 1}},
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
		{Pricing: model.Pricing{ModelName: "image-a", QuotaType: 1, ModelPrice: 0.3}, ModelType: "image", SyncEnabled: true, GroupRatios: map[string]float64{"group-a": 0.11}},
	}}
	original, err := branchCatalogVersion(&base)
	require.NoError(t, err)
	base.GroupRatio["group-b"] = 2
	unchanged, err := branchCatalogVersion(&base)
	require.NoError(t, err)
	require.Equal(t, original, unchanged)
	base.Models[0].GroupRatios["group-a"] = 0.5
	changed, err := branchCatalogVersion(&base)
	require.NoError(t, err)
	require.NotEqual(t, original, changed)
	base.Models[0].GroupRatios = map[string]float64{"group-b": 1.2}
	changed, err = branchCatalogVersion(&base)
	require.NoError(t, err)
	require.NotEqual(t, original, changed)
}
