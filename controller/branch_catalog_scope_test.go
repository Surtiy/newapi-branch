package controller

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// A previously unseen upstream user key is accepted through the same public
// APIs as every other key. Branch management still requires local permission.
func TestBranchCatalogNewUserKeyConfiguresAndRefreshesFromAPIs(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Option{}, &model.Log{}, &model.AuditLog{}))
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	group := "ordinary-user-group"
	http.DefaultTransport = branchCatalogTransport(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, "Bearer sk-new-user-private-key", r.Header.Get("Authorization"))
		require.Equal(t, "aicost.me", r.URL.Host)
		require.Empty(t, r.Header.Get("New-Api-User"))
		if r.URL.Path == "/v1/models" {
			return branchCatalogModelsResponse(t, r, "image"), nil
		}
		require.Equal(t, "/api/distributor/models", r.URL.Path)
		body := fmt.Sprintf(`{"success":true,"data":{"catalog_version":"v1","models":[{"model_name":"image","sync_enabled":true,"group_ratios":{"%s":0.11}},{"model_name":"not-allowed","sync_enabled":true,"group_ratios":{"outside":1.2}}]}}`, group)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	call := func(handler func(*gin.Context), body string) string {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set("id", 1)
		c.Set("role", common.RoleRootUser)
		c.Request = httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		handler(c)
		require.Contains(t, w.Body.String(), `"success":true`, w.Body.String())
		require.NotContains(t, w.Body.String(), "sk-new-user-private-key")
		require.NotContains(t, w.Body.String(), "outside")
		return w.Body.String()
	}
	call(ConfigureBranchCatalog, `{"api_key":"sk-new-user-private-key"}`)
	var source model.Channel
	require.NoError(t, db.First(&source).Error)
	require.Equal(t, []string{group}, model.BranchCatalogBoundGroups(&source))
	call(PreviewBranchCatalog, fmt.Sprintf(`{"channel_id":%d}`, source.Id))
	// Changes in the complete API snapshot update the source without operator
	// files, SSH access, or approval of the new group on the branch machine.
	group = "new-directory-group"
	call(PreviewBranchCatalog, fmt.Sprintf(`{"channel_id":%d}`, source.Id))
	require.NoError(t, db.First(&source, source.Id).Error)
	require.Equal(t, []string{group}, model.BranchCatalogBoundGroups(&source))
}

func TestBranchCatalogRejectsInvalidUpstreamKeyWithoutSaving(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Option{}, &model.AuditLog{}))
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = branchCatalogTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader("private-upstream-response")), Request: r}, nil
	})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("role", common.RoleRootUser)
	c.Request = httptest.NewRequest(http.MethodPost, "/config", strings.NewReader(`{"api_key":"sk-invalid-private-key"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	ConfigureBranchCatalog(c)
	require.Contains(t, w.Body.String(), `"success":false`)
	require.Contains(t, w.Body.String(), "401")
	require.NotContains(t, w.Body.String(), "private")
	var count int64
	require.NoError(t, db.Model(&model.Channel{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestBranchCatalogDirectoryGroupsAreCandidatesNotVerifiedBindings(t *testing.T) {
	catalog := &branchCatalog{Models: []branchCatalogItem{
		{Pricing: model.Pricing{ModelName: "shared"}, SyncEnabled: true, GroupRatios: map[string]float64{"group-a": 0.11, "group-b": 1.2}},
	}}
	require.NoError(t, scopeBranchCatalog(catalog))
	groups, err := branchCatalogGroups(catalog)
	require.NoError(t, err)
	require.Equal(t, []string{"group-a", "group-b"}, groups)
	// Never arbitrarily select the first/cheapest group and call it a binding.
	require.Equal(t, map[string]float64{"group-a": 0.11, "group-b": 1.2}, catalog.Models[0].GroupRatios)
	groups, err = branchCatalogGroups(&branchCatalog{})
	require.NoError(t, err)
	require.Empty(t, groups)
}

func TestBranchCatalogFilteringDropsUnboundModelsAndKeepsExactRatios(t *testing.T) {
	catalog := branchCatalog{Models: []branchCatalogItem{
		{Pricing: model.Pricing{ModelName: "shared"}, GroupRatios: map[string]float64{"kiro": 0.11, "full": 1.2, "retail": 0.5}},
		{Pricing: model.Pricing{ModelName: "outside"}, GroupRatios: map[string]float64{"full": 1.2}},
	}}
	filterBranchCatalogGroups(&catalog, []string{"kiro"})
	require.Len(t, catalog.Models, 1)
	require.Equal(t, map[string]float64{"kiro": 0.11}, catalog.GroupRatio)
	require.Equal(t, []string{"kiro"}, catalog.Models[0].EnableGroup)
	filterBranchCatalogGroups(&catalog, nil)
	require.Empty(t, catalog.Models)
}
