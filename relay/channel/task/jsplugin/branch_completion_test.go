package jsplugin

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBranchVideoSubmitsOnlyReferenceURLs(t *testing.T) {
	source, err := os.ReadFile("../../../../plugins/tasks/aicost-branch/plugin.js")
	require.NoError(t, err)
	plugin, err := pluginruntime.NewRegistry().Register(string(source), pluginruntime.Options{})
	require.NoError(t, err)
	original := system_setting.ServerAddress
	system_setting.ServerAddress = "https://branch.example"
	previousFetch := *system_setting.GetFetchSetting()
	system_setting.GetFetchSetting().AllowPrivateIp = true
	system_setting.GetFetchSetting().AllowedPorts = []string{"1-65535"}
	t.Cleanup(func() { system_setting.ServerAddress = original; *system_setting.GetFetchSetting() = previousFetch })
	service.InitHttpClient()
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 32)...)
	wav := append([]byte("RIFF\x24\x00\x00\x00WAVEfmt "), bytes.Repeat([]byte{0}, 32)...)
	mp4 := []byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm', 0, 0, 0, 0, 'i', 's', 'o', 'm', 'm', 'p', '4', '2'}
	for _, format := range []string{"json-base64", "multipart"} {
		t.Run(format, func(t *testing.T) {
			directory := t.TempDir()
			t.Setenv("CANVAS_ASSET_DIR", directory)
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				assert.Equal(t, "/v1/videos", r.URL.Path)
				assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
				assert.Equal(t, "Bearer fixture-upstream-key", r.Header.Get("Authorization"))
				var submitted map[string]any
				require.NoError(t, common.DecodeJson(r.Body, &submitted))
				assert.Equal(t, "video-2.5-101010", submitted["model"])
				assert.Equal(t, "完整提示词 @图片1 @视频1 @音频1", submitted["prompt"])
				assert.Equal(t, float64(4), submitted["seconds"])
				assert.Equal(t, map[string]any{"enabled": false, "mode": "light"}, submitted["face"])
				for _, field := range []string{"images_base64", "audios_base64", "videos_base64"} {
					assert.NotContains(t, submitted, field)
				}
				for field, expected := range map[string][]byte{"image_urls": png, "video_urls": mp4, "audio_urls": wav} {
					values, ok := submitted[field].([]any)
					require.True(t, ok, field)
					require.NotEmpty(t, values)
					for _, value := range values {
						parsed, err := url.Parse(value.(string))
						require.NoError(t, err)
						assert.Equal(t, "https", parsed.Scheme)
						assert.Equal(t, "branch.example", parsed.Hostname())
						actual, err := os.ReadFile(filepath.Join(directory, filepath.Base(parsed.Path)))
						require.NoError(t, err)
						assert.Equal(t, expected, actual)
					}
				}
				_, _ = io.WriteString(w, `{"id":"mock-upstream-task","status":"queued"}`)
			}))
			defer upstream.Close()
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Set("id", 7)
			body := map[string]any{"model": "video-2.5-101010", "prompt": "完整提示词 @图片1 @视频1 @音频1", "seconds": 4, "face": map[string]any{"enabled": false, "mode": "light"}}
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
			if format == "json-base64" {
				for field, data := range map[string][]byte{"images_base64": png, "videos_base64": mp4, "audios_base64": wav} {
					body[field] = []any{base64.StdEncoding.EncodeToString(data)}
				}
				c.Request.Header.Set("Content-Type", "application/json")
			} else {
				body["face"] = `{"enabled":false,"mode":"light"}`
				var input bytes.Buffer
				writer := multipart.NewWriter(&input)
				for _, item := range []struct {
					field string
					data  []byte
				}{{"input_reference[]", png}, {"input_reference[]", png}, {"video", mp4}, {"audio", wav}} {
					part, err := writer.CreateFormFile(item.field, "fixture.bin")
					require.NoError(t, err)
					_, err = part.Write(item.data)
					require.NoError(t, err)
				}
				require.NoError(t, writer.Close())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", &input)
				c.Request.Header.Set("Content-Type", writer.FormDataContentType())
			}
			c.Set("task_request", body)
			info := &relaycommon.RelayInfo{OriginModelName: "video-2.5-101010", ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: upstream.URL, ApiKey: "fixture-upstream-key"}, TaskRelayInfo: &relaycommon.TaskRelayInfo{}}
			adaptor := New(plugin)
			adaptor.Init(info)
			require.Nil(t, adaptor.ValidateRequestAndSetAction(c, info))
			requestBody, err := adaptor.BuildRequestBody(c, info)
			require.NoError(t, err)
			response, err := adaptor.DoRequest(c, info, requestBody)
			require.NoError(t, err)
			defer response.Body.Close()
			assert.Equal(t, http.StatusOK, response.StatusCode)
			assert.Equal(t, 1, calls)
			entries, err := os.ReadDir(directory)
			require.NoError(t, err)
			assert.Len(t, entries, map[string]int{"json-base64": 3, "multipart": 4}[format])
			// Repeated validation/build uses the cached URL body, not another upload.
			again, err := adaptor.BuildRequestBody(c, info)
			require.NoError(t, err)
			data, err := io.ReadAll(again)
			require.NoError(t, err)
			assert.NotContains(t, string(data), "base64")
			assert.NotContains(t, string(data), "request_file:")
			assert.False(t, strings.HasPrefix(string(data), "--"))
		})
	}
}

func TestBranchCanvasUpscaleBillingContext(t *testing.T) {
	source, err := os.ReadFile("../../../../plugins/tasks/aicost-branch/plugin.js")
	require.NoError(t, err)
	plugin, err := pluginruntime.NewRegistry().Register(string(source), pluginruntime.Options{})
	require.NoError(t, err)
	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		if key == "billing_setting.billing_mode" || key == "billing_setting.billing_expr" {
			saved[key] = value
		}
		return nil
	}))
	t.Cleanup(func() { require.NoError(t, config.GlobalConfig.LoadFromDB(saved)) })
	for _, tc := range []struct {
		name, expression string
		wantError        bool
	}{
		{"fixed", `tier("request", 0.8)`, false},
		{"seconds", `tier("second", u("seconds") * 0.12)`, true},
		{"dynamic-usage", `tier("request", u("seconds") * 0.8)`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exprs, err := common.Marshal(map[string]string{"example-视频超分": tc.expression})
			require.NoError(t, err)
			require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"billing_setting.billing_mode": `{"example-视频超分":"tiered_expr"}`, "billing_setting.billing_expr": string(exprs)}))
			info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: "https://provider.example", ApiKey: "private-key"}, OriginModelName: "example-视频超分", TaskRelayInfo: &relaycommon.TaskRelayInfo{}}
			adaptor := New(plugin)
			adaptor.Init(info)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
			c.Request.Header.Set("Idempotency-Key", "upscale-client-id")
			body := map[string]any{"model": "example-视频超分", "videos": []any{"https://cdn.example/source.mp4"}, "resolution": "1080p", "fps": 30, "allowMissingDuration": true}
			c.Set("task_request", body)
			facts, err := adaptor.ExtractUsageFactsValidated(c, info)
			if tc.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Empty(t, facts, "flat-price upscaling must not fabricate duration usage")
			ratios, err := adaptor.EstimateBillingValidated(c, info)
			require.NoError(t, err)
			assert.Empty(t, ratios)
			request, err := adaptor.buildSubmit(c, info)
			require.NoError(t, err)
			assert.Equal(t, "upscale-client-id", request.Headers["Idempotency-Key"])
			assert.Equal(t, "https://provider.example/v1/videos", request.URL)
			encoded, err := common.Marshal(request.Body)
			require.NoError(t, err)
			assert.NotContains(t, string(encoded), `"seconds"`)
			assert.NotContains(t, string(encoded), `"prompt"`)
		})
	}
}

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
