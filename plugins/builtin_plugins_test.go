package plugins

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var expectedKeys = []string{"alibaba", "doubao", "google", "hailuo", "jimeng", "kling", "sora", "sunoapi", "vertex-ai", "vidu"}

func TestBuiltInAICostBranchFactoryWithoutDatabase(t *testing.T) {
	plugin, found := jsplugin.DefaultRegistry.Generation().Get("aicost-branch")
	require.True(t, found, "embedded factory must be available before any database import")
	assert.NotContains(t, jsplugin.DefaultRegistry.OverridePlugins(), "aicost-branch")
	source, err := Source("aicost-branch")
	require.NoError(t, err)
	registry := jsplugin.NewRegistry()
	plugin, err = registry.RegisterFactory(source, jsplugin.Options{Key: "aicost-branch"})
	require.NoError(t, err)
	assert.Equal(t, []string{"aicost-branch-video"}, plugin.Meta.Models)
	assert.Equal(t, []jsplugin.ProtocolClaim{{Name: "openai_video"}}, plugin.Meta.Protocols)
	// Only model-bearing create operations are indexed; queries resolve by task.
	binding, found := registry.Generation().LookupEndpoint("POST", "/v1/videos", "aicost-branch-video")
	require.True(t, found)
	assert.Same(t, plugin, binding.Plugin)
	schema, ok := plugin.Meta.UsageSchema["seconds"]
	require.True(t, ok)
	assert.Equal(t, "number", schema.Type)
	assert.Equal(t, "second", schema.Unit)
	decoded, err := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_video", "decodeRequest"}, map[string]any{
		"model": "seedance2.0-720p", "upstreamModel": "aicost-branch-video",
		"body": map[string]any{"kind": "json", "value": map[string]any{"prompt": "@角色A/#普通话", "seconds": 30}},
	})
	require.NoError(t, err)
	encoded, err := common.Marshal(decoded)
	require.NoError(t, err)
	var request map[string]any
	require.NoError(t, common.Unmarshal(encoded, &request))
	assert.Equal(t, "seedance2.0-720p", request["model"])
	submit, err := plugin.Engine.Call(t.Context(), "buildSubmitRequest", map[string]any{
		"model": request["model"], "requestBody": request["requestBody"],
		"baseUrl": "https://upstream.example", "apiKey": "test-key",
	})
	require.NoError(t, err)
	encoded, err = common.Marshal(submit)
	require.NoError(t, err)
	var upstream map[string]any
	require.NoError(t, common.Unmarshal(encoded, &upstream))
	assert.Equal(t, "https://upstream.example/v1/videos", upstream["url"])
	assert.Equal(t, map[string]any{"model": "seedance2.0-720p", "prompt": "@角色A/#普通话", "seconds": float64(30)}, upstream["body"])

	value, err := plugin.Engine.Call(t.Context(), "extractUsage", map[string]any{"requestBody": map[string]any{"seconds": "30"}})
	require.NoError(t, err)
	encoded, err = common.Marshal(value)
	require.NoError(t, err)
	var usage map[string]any
	require.NoError(t, common.Unmarshal(encoded, &usage))
	assert.Equal(t, map[string]any{"seconds": float64(30)}, usage)
	for _, test := range []struct {
		expr string
		want float64
	}{{`tier("request", 3.3)`, 3.3}, {`tier("second", u("seconds") * 0.4)`, 12}} {
		require.NoError(t, billing_setting.SmokeTestTaskExpr(test.expr, plugin.Meta.UsageSchema))
		cost, _, err := billingexpr.RunExprWithRequest(test.expr, billingexpr.TokenParams{}, billingexpr.RequestInput{Usage: usage})
		require.NoError(t, err)
		assert.Equal(t, test.want, cost)
	}
	assert.Error(t, billing_setting.SmokeTestTaskExpr(`u("undeclared_usage")`, plugin.Meta.UsageSchema))
}

func TestBuiltInAICostBranchOverrideFallbackAndSwitches(t *testing.T) {
	source, err := Source("aicost-branch")
	require.NoError(t, err)
	registry := jsplugin.NewRegistry()
	factory, err := registry.RegisterFactory(source, jsplugin.Options{})
	require.NoError(t, err)
	overrideSource := strings.Replace(source, "version: '"+factory.Meta.Version+"'", "version: '"+factory.Meta.Version+"-custom'", 1)
	override, err := jsplugin.CompilePlugin(overrideSource, jsplugin.Options{})
	require.NoError(t, err)
	require.NoError(t, registry.ReplaceOverrides([]*jsplugin.LoadedPlugin{override}))
	active, found := registry.Get("aicost-branch")
	require.True(t, found)
	assert.Same(t, override, active)
	assert.NotEqual(t, factory.Meta.Version, active.Meta.Version)

	// Refreshing the bundled factory leaves the administrator's override intact.
	factory, err = registry.RegisterFactory(source, jsplugin.Options{})
	require.NoError(t, err)
	active, found = registry.Get("aicost-branch")
	require.True(t, found)
	assert.Same(t, override, active)
	registry.SetEnabled(false)
	_, found = registry.Get("aicost-branch")
	assert.False(t, found)
	_, found = registry.Generation().LookupEndpoint("POST", "/v1/videos", "aicost-branch-video")
	assert.False(t, found)
	registry.SetEnabled(true)
	active, found = registry.Get("aicost-branch")
	require.True(t, found)
	assert.Same(t, override, active)

	// A database sync removing an override restores the bundled implementation.
	require.NoError(t, registry.ReplaceOverrides(nil))
	active, found = registry.Get("aicost-branch")
	require.True(t, found)
	assert.Same(t, factory, active)
	binding, found := registry.Generation().LookupEndpoint("POST", "/v1/videos", "aicost-branch-video")
	require.True(t, found)
	assert.Same(t, factory, binding.Plugin)

	registry.SetDisabledFactoryKeys([]string{"aicost-branch"})
	_, found = registry.Get("aicost-branch")
	assert.False(t, found)
	_, found = registry.Generation().LookupEndpoint("POST", "/v1/videos", "aicost-branch-video")
	assert.False(t, found)
	require.NoError(t, registry.ReplaceOverrides([]*jsplugin.LoadedPlugin{override}))
	active, found = registry.Get("aicost-branch")
	require.True(t, found)
	assert.Same(t, override, active, "disabling the factory must not remove an enabled override")
	require.NoError(t, registry.Unregister("aicost-branch"))
	_, found = registry.Get("aicost-branch")
	assert.False(t, found, "removing an override must respect the disabled factory")
	registry.SetDisabledFactoryKeys(nil)
	active, found = registry.Get("aicost-branch")
	require.True(t, found)
	assert.Same(t, factory, active)
}

func TestBuiltInAICostBranchContentOnlyCompletion(t *testing.T) {
	source, err := Source("aicost-branch")
	require.NoError(t, err)
	plugin, err := jsplugin.NewRegistry().RegisterFactory(source, jsplugin.Options{})
	require.NoError(t, err)
	for _, status := range []string{"completed", "succeeded", "success"} {
		value, err := plugin.Engine.Call(t.Context(), "parseTaskResult", map[string]any{}, map[string]any{"status": status})
		require.NoError(t, err)
		assert.Equal(t, map[string]any{"status": "SUCCESS", "progress": "100%"}, value)
	}
	for _, method := range []string{"GET", "HEAD"} {
		value, err := plugin.Engine.Call(t.Context(), "buildContentRequest", map[string]any{
			"taskId": "public-branch-id", "upstreamTaskId": "upstream/id?#",
			"artifactKey": "video", "baseUrl": "https://upstream.example/gateway/", "apiKey": "test-key",
			"data":          map[string]any{"status": "completed", "video_url": "https://untrusted.example/expired.mp4"},
			"clientRequest": map[string]any{"method": method},
		})
		require.NoError(t, err)
		assert.Equal(t, map[string]any{
			"url":    "https://upstream.example/gateway/v1/videos/upstream%2Fid%3F%23/content",
			"method": method, "headers": map[string]any{"Authorization": "Bearer test-key"},
		}, value)
	}
}

func TestBuiltInVendorPluginsDeclareNativeRoutesAndLegacyChannelTypes(t *testing.T) {
	generation := jsplugin.DefaultRegistry.Generation()
	require.NotNil(t, generation)

	routes := []struct {
		method    string
		path      string
		key       string
		routeType jsplugin.RouteType
		action    string
		renderer  string
	}{
		{"POST", "/kling/v1/videos/text2video", "kling", jsplugin.RouteTypeSubmit, "text_to_video", "taskCreated"},
		{"POST", "/kling/v1/videos/image2video", "kling", jsplugin.RouteTypeSubmit, "image_to_video", "taskCreated"},
		{"GET", "/kling/v1/videos/text2video/:task_id", "kling", jsplugin.RouteTypeQuery, "", "taskStatus"},
		{"GET", "/kling/v1/videos/image2video/:task_id", "kling", jsplugin.RouteTypeQuery, "", "taskStatus"},
		{"POST", "/jimeng/", "jimeng", jsplugin.RouteTypeDynamic, "", "renderTask"},
		{"POST", "/suno/submit/:action", "sunoapi", jsplugin.RouteTypeSubmit, "", "renderSubmit"},
		{"POST", "/suno/fetch", "sunoapi", jsplugin.RouteTypeDynamic, "", "renderTasks"},
		{"GET", "/suno/fetch/:task_id", "sunoapi", jsplugin.RouteTypeQuery, "", "renderTask"},
		{"POST", "/doubao/api/v3/contents/generations/tasks", "doubao", jsplugin.RouteTypeSubmit, "", "taskCreated"},
		{"GET", "/doubao/api/v3/contents/generations/tasks/:task_id", "doubao", jsplugin.RouteTypeQuery, "", "taskStatus"},
	}
	for _, expected := range routes {
		t.Run(expected.method+" "+expected.path, func(t *testing.T) {
			binding, found := generation.LookupDeclaredRoute(expected.method, expected.path)
			require.True(t, found)
			require.Equal(t, expected.key, binding.Plugin.Meta.Key)
			require.Equal(t, expected.routeType, binding.Route.Type)
			require.Equal(t, expected.action, binding.Route.Action)
			require.Equal(t, expected.renderer, binding.Route.Render)
		})
	}

	channelTypes := []struct {
		value int
		key   string
	}{
		{1, "sora"},
		{36, "sunoapi"},
		{45, "doubao"},
		{50, "kling"},
		{51, "jimeng"},
		{54, "doubao"},
		{55, "sora"},
	}
	for _, channelType := range channelTypes {
		plugin, found := generation.GetByChannelType(channelType.value)
		require.True(t, found)
		require.Equal(t, channelType.key, plugin.Meta.Key)
	}
}

func TestBuiltInTaskPluginResponsesAndUsageContracts(t *testing.T) {
	generation := jsplugin.DefaultRegistry.Generation()
	require.NotNil(t, generation)

	entries, err := fs.ReadDir(taskPlugins, "tasks")
	require.NoError(t, err)
	actualKeys := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			actualKeys = append(actualKeys, entry.Name())
		}
	}
	// The branch adapter is video-only; vendor Responses contracts below remain unchanged.
	allKeys := append([]string{"aicost-branch"}, expectedKeys...)
	assert.Equal(t, allKeys, actualKeys)

	for _, key := range expectedKeys {
		t.Run(key, func(t *testing.T) {
			_, found := generation.Get(key)
			require.True(t, found, "factory plugin was excluded from the active generation")

			source, sourceErr := Source(key)
			require.NoError(t, sourceErr)
			registry := jsplugin.NewRegistry()
			plugin, registerErr := registry.RegisterFactory(source, jsplugin.Options{Key: key})
			require.NoError(t, registerErr)

			var responsesClaim jsplugin.ProtocolClaim
			foundResponses := false
			for _, claim := range plugin.Meta.Protocols {
				if claim.Name == "openai_responses" {
					responsesClaim = claim
					foundResponses = true
					break
				}
			}
			require.True(t, foundResponses, "openai_responses claim must be present")
			assert.Equal(t, []string{"stream", "sync", "background"}, responsesClaim.Supports)
			for _, model := range plugin.Meta.Models {
				binding, claimed := registry.Generation().LookupEndpoint("POST", "/v1/responses", model)
				require.True(t, claimed, model)
				assert.Same(t, plugin, binding.Plugin)
			}
			for _, hook := range []string{"decodeRequest", "renderEvents", "renderFinal"} {
				callable, callableErr := plugin.Engine.HasCallablePath(t.Context(), "protocols", "openai_responses", hook)
				require.NoError(t, callableErr)
				assert.True(t, callable, hook)
			}
			for _, hook := range []string{"extractUsage", "extractUsageOnComplete"} {
				callable, callableErr := plugin.Engine.HasExport(t.Context(), hook)
				require.NoError(t, callableErr)
				assert.True(t, callable, hook)
			}
			require.NotEmpty(t, plugin.Meta.UsageSchema)
			for usageKey, schema := range plugin.Meta.UsageSchema {
				assert.NotEmpty(t, schema.Description, usageKey)
			}
		})
	}
}

func TestBuiltInResponsesDecodersEchoChannelMappedAlias(t *testing.T) {
	bodyOverrides := map[string]map[string]any{}
	for _, key := range expectedKeys {
		t.Run(key, func(t *testing.T) {
			source, sourceErr := Source(key)
			require.NoError(t, sourceErr)
			registry := jsplugin.NewRegistry()
			plugin, registerErr := registry.RegisterFactory(source, jsplugin.Options{Key: key})
			require.NoError(t, registerErr)
			require.NotEmpty(t, plugin.Meta.Models)

			alias := "alias-under-test"
			upstreamModel := plugin.Meta.Models[0]
			body := map[string]any{"model": alias, "input": "a cat walking on the beach"}
			if override, ok := bodyOverrides[key]; ok {
				body = override
			}
			value, callErr := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_responses", "decodeRequest"}, map[string]any{
				"model": alias, "upstreamModel": upstreamModel, "stream": false,
				"body": map[string]any{"kind": "json", "value": body},
			})
			require.NoError(t, callErr)
			encoded, marshalErr := common.Marshal(value)
			require.NoError(t, marshalErr)
			var decoded map[string]any
			require.NoError(t, common.Unmarshal(encoded, &decoded))
			assert.Equal(t, "submit", decoded["kind"])
			assert.Equal(t, alias, decoded["model"])
		})
	}
}
