package jsplugin

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBranchVideoCompletionRequiresReadableMedia(t *testing.T) {
	source, err := os.ReadFile("../../../../plugins/tasks/aicost-branch/plugin.js")
	require.NoError(t, err)
	plugin, err := pluginruntime.NewRegistry().Register(string(source), pluginruntime.Options{})
	require.NoError(t, err)
	originalFetch := *system_setting.GetFetchSetting()
	system_setting.GetFetchSetting().EnableSSRFProtection = true
	system_setting.GetFetchSetting().AllowPrivateIp = true
	system_setting.GetFetchSetting().AllowedPorts = []string{"1-65535"}
	t.Cleanup(func() { *system_setting.GetFetchSetting() = originalFetch })
	service.InitHttpClient()
	mp4 := []byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm', 0, 0, 0, 0, 'i', 's', 'o', 'm', 'm', 'p', '4', '2'}
	for _, tc := range []struct {
		name   string
		status int
		mime   string
		body   []byte
		want   string
	}{
		{"video-206", 206, "video/mp4", mp4, "SUCCESS"},
		{"video-200", 200, "video/mp4", mp4, "SUCCESS"},
		{"binary-with-video-signature", 200, "application/octet-stream", mp4, "SUCCESS"},
		{"empty", 200, "video/mp4", nil, "IN_PROGRESS"},
		{"json", 200, "application/json", []byte(`{"url":"video.mp4"}`), "IN_PROGRESS"},
		{"html", 200, "text/html", []byte(`<html>login</html>`), "IN_PROGRESS"},
		{"fake-mime", 200, "video/mp4", []byte(`{"error":"not ready"}`), "IN_PROGRESS"},
		{"auth", 401, "application/json", nil, "IN_PROGRESS"},
		{"forbidden", 403, "application/json", nil, "IN_PROGRESS"},
		{"not-ready", 404, "application/json", nil, "IN_PROGRESS"},
		{"accepted-not-ready", 202, "video/mp4", mp4, "IN_PROGRESS"},
		{"rate-limit", 429, "application/json", nil, "IN_PROGRESS"},
		{"upstream-error", 503, "application/json", nil, "IN_PROGRESS"},
		{"redirect", 302, "video/mp4", mp4, "IN_PROGRESS"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				assert.Equal(t, "/v1/videos/upstream-id/content", r.URL.Path)
				assert.Equal(t, "Bearer original-key", r.Header.Get("Authorization"))
				assert.Equal(t, "bytes=0-511", r.Header.Get("Range"))
				w.Header().Set("Content-Type", tc.mime)
				w.Header().Set("Location", "https://untrusted.invalid/content")
				w.WriteHeader(tc.status)
				_, _ = w.Write(tc.body)
			}))
			defer server.Close()
			adaptor := New(plugin)
			adaptor.Init(&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: server.URL, ApiKey: "rotated-key"}})
			task := &model.Task{TaskID: "branch-id", PrivateData: model.TaskPrivateData{UpstreamTaskID: "upstream-id", Key: "original-key"}}
			result, err := adaptor.ParseTaskResult(task, &http.Response{StatusCode: 200}, []byte(`{"status":"completed"}`))
			require.NoError(t, err)
			assert.Equal(t, tc.want, result.Status)
			assert.Equal(t, 1, calls)
			if tc.want == "IN_PROGRESS" {
				assert.Equal(t, "99%", result.Progress)
				assert.Empty(t, result.Url)
			}
			// Legacy common envelopes use the same readiness gate.
			legacy := &relaycommon.TaskInfo{Status: "SUCCESS", Url: "https://stale.invalid/video.mp4"}
			adaptor.CheckTaskCompletion(task, legacy)
			assert.Equal(t, tc.want, legacy.Status)
			assert.Equal(t, 2, calls)
			for _, body := range []string{`{"status":"processing"}`, `{"status":"failed"}`} {
				_, err := adaptor.ParseTaskResult(task, &http.Response{StatusCode: 200}, []byte(body))
				require.NoError(t, err)
			}
			assert.Equal(t, 2, calls, "only completed responses may trigger a media check")
		})
	}

	t.Run("recovery-from-wait", func(t *testing.T) {
		ready := false
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !ready {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(mp4)
		}))
		defer server.Close()
		adaptor := New(plugin)
		adaptor.Init(&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: server.URL, ApiKey: "key"}})
		task := &model.Task{TaskID: "public", PrivateData: model.TaskPrivateData{UpstreamTaskID: "upstream-id"}}
		for _, want := range []string{"IN_PROGRESS", "SUCCESS"} {
			result, err := adaptor.ParseTaskResult(task, &http.Response{StatusCode: 200}, []byte(`{"status":"completed","video_url":"https://stale.invalid/video.mp4"}`))
			require.NoError(t, err)
			assert.Equal(t, want, result.Status)
			ready = true
		}
	})

	t.Run("transport-error", func(t *testing.T) {
		server := httptest.NewServer(http.NotFoundHandler())
		server.Close()
		adaptor := New(plugin)
		adaptor.Init(&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: server.URL, ApiKey: "key"}})
		result, err := adaptor.ParseTaskResult(&model.Task{TaskID: "upstream-id"}, &http.Response{StatusCode: 200}, []byte(`{"status":"completed"}`))
		require.NoError(t, err)
		assert.Equal(t, "IN_PROGRESS", result.Status)
	})

	t.Run("probe-closes-after-prefix-without-downloading-rest", func(t *testing.T) {
		closed := make(chan struct{}, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "video/mp4")
			prefix := make([]byte, 512)
			copy(prefix, mp4)
			_, _ = w.Write(prefix)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			closed <- struct{}{}
		}))
		defer server.Close()
		adaptor := New(plugin)
		adaptor.Init(&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: server.URL, ApiKey: "key"}})
		result, err := adaptor.ParseTaskResult(&model.Task{TaskID: "upstream-id"}, &http.Response{StatusCode: 200}, []byte(`{"status":"completed"}`))
		require.NoError(t, err)
		assert.Equal(t, "SUCCESS", result.Status)
		select {
		case <-closed:
		case <-time.After(2 * time.Second):
			t.Fatal("probe kept the full media stream open")
		}
	})

	t.Run("blocked-probe-destinations", func(t *testing.T) {
		for _, target := range []string{"https://other.invalid/content", "http://provider.invalid/content", "https://user:pass@provider.invalid/content", "https://provider.invalid/content#fragment"} {
			ctx := map[string]any{"baseUrl": "https://provider.invalid", "target": target}
			checkSource := mockPlugin + `
export function buildCompletionCheckRequest(ctx) { return {url: ctx.target, method: "GET"}; }
`
			checkPlugin, err := pluginruntime.NewRegistry().Register(checkSource, pluginruntime.Options{})
			require.NoError(t, err)
			assert.False(t, New(checkPlugin).completionVideoReady(ctx, "https://provider.invalid", ""), target)
		}
	})
}
