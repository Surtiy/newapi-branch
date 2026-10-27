package service

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

	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCanvasVideoReferencesURLNormalization(t *testing.T) {
	gin.SetMode(gin.TestMode)
	original := system_setting.ServerAddress
	system_setting.ServerAddress = "https://branch.example/gateway"
	t.Cleanup(func() { system_setting.ServerAddress = original })
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 32)...)
	wav := append([]byte("RIFF\x24\x00\x00\x00WAVEfmt "), bytes.Repeat([]byte{0}, 32)...)
	mp4 := []byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm', 0, 0, 0, 0, 'i', 's', 'o', 'm', 'm', 'p', '4', '2'}
	dataURL := func(kind string, data []byte) string {
		return "data:" + kind + ";base64," + base64.StdEncoding.EncodeToString(data)
	}
	context := func() *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Set("id", 7)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
		return c
	}
	check := func(t *testing.T, value any, expected []byte) {
		t.Helper()
		assetURL, ok := value.(string)
		require.True(t, ok)
		require.True(t, strings.HasPrefix(assetURL, "https://branch.example/gateway/v1/assets/"))
		parsed, err := url.Parse(assetURL)
		require.NoError(t, err)
		actual, err := os.ReadFile(filepath.Join(CanvasAssetDirectory(), filepath.Base(parsed.Path)))
		require.NoError(t, err)
		assert.Equal(t, expected, actual)
	}
	t.Run("base64-and-existing-url", func(t *testing.T) {
		t.Setenv("CANVAS_ASSET_DIR", t.TempDir())
		body := map[string]any{
			"model": "video-2.5-101010", "prompt": "完整提示词 @图片1 @音频1 @视频1", "seconds": 4,
			"face": map[string]any{"enabled": false}, "resolution": "720p", "client_task_id": "original-id",
			"image_urls": []any{"https://cdn.example/original.png"}, "images_base64": []any{dataURL("image/png", png)},
			"audios_base64": []any{dataURL("audio/wav", wav)}, "videos_base64": []any{dataURL("video/mp4", mp4)},
		}
		c := context()
		converted, err := NormalizeCanvasVideoReferences(c, body)
		require.NoError(t, err)
		images := converted["image_urls"].([]any)
		require.Len(t, images, 2)
		assert.Equal(t, "https://cdn.example/original.png", images[0])
		check(t, images[1], png)
		check(t, converted["audio_urls"].([]any)[0], wav)
		check(t, converted["video_urls"].([]any)[0], mp4)
		for _, key := range []string{"images_base64", "audios_base64", "videos_base64"} {
			assert.NotContains(t, converted, key)
			assert.Contains(t, body, key, "do not mutate the original client request")
		}
		for _, key := range []string{"model", "prompt", "seconds", "face", "resolution", "client_task_id"} {
			assert.Equal(t, body[key], converted[key])
		}
		again, err := NormalizeCanvasVideoReferences(c, body)
		require.NoError(t, err)
		assert.Equal(t, converted, again)
		entries, err := os.ReadDir(CanvasAssetDirectory())
		require.NoError(t, err)
		assert.Len(t, entries, 3, "prebilling/build/retry reuse published URLs")
	})
	t.Run("model-fields-role-and-duplicate-references", func(t *testing.T) {
		t.Setenv("CANVAS_ASSET_DIR", t.TempDir())
		image := dataURL("image/png", png)
		body := map[string]any{
			"prompt":      "keep data:image/png;base64,AAAA in text",
			"start_frame": image, "end_frame": image,
			"reference_images": []any{map[string]any{"url": image, "name": "人物A", "role": "material"}},
			"input_video":      dataURL("video/mp4", mp4), "audio_reference": []any{base64.StdEncoding.EncodeToString(wav)},
			"content": []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": image}, "role": "first_frame"}, map[string]any{"type": "text", "text": "keep text"}},
		}
		converted, err := NormalizeCanvasVideoReferences(context(), body)
		require.NoError(t, err)
		check(t, converted["start_frame"], png)
		assert.Equal(t, converted["start_frame"], converted["end_frame"])
		imageObject := converted["reference_images"].([]any)[0].(map[string]any)
		check(t, imageObject["url"], png)
		assert.Equal(t, "material", imageObject["role"])
		assert.Equal(t, "人物A", imageObject["name"])
		check(t, converted["input_video"], mp4)
		check(t, converted["audio_reference"].([]any)[0], wav)
		assert.Equal(t, body["prompt"], converted["prompt"])
		content := converted["content"].([]any)
		check(t, content[0].(map[string]any)["image_url"].(map[string]any)["url"], png)
		assert.Equal(t, "first_frame", content[0].(map[string]any)["role"])
		entries, err := os.ReadDir(CanvasAssetDirectory())
		require.NoError(t, err)
		assert.Len(t, entries, 3, "repeated references share one published file")
	})
	t.Run("multipart-repeated-files-and-json-fields", func(t *testing.T) {
		t.Setenv("CANVAS_ASSET_DIR", t.TempDir())
		var input bytes.Buffer
		writer := multipart.NewWriter(&input)
		for _, item := range []struct {
			field string
			data  []byte
		}{{"input_reference[]", png}, {"input_reference[]", png}, {"video", mp4}, {"audio", wav}} {
			part, err := writer.CreateFormFile(item.field, "reference.bin")
			require.NoError(t, err)
			_, err = part.Write(item.data)
			require.NoError(t, err)
		}
		require.NoError(t, writer.Close())
		c := context()
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", &input)
		c.Request.Header.Set("Content-Type", writer.FormDataContentType())
		converted, err := NormalizeCanvasVideoReferences(c, map[string]any{"image_urls": `["https://cdn.example/first.png"]`, "prompt": "do not change", "seconds": "4"})
		require.NoError(t, err)
		images := converted["image_urls"].([]any)
		require.Len(t, images, 3)
		check(t, images[1], png)
		check(t, images[2], png)
		check(t, converted["video_urls"].([]any)[0], mp4)
		check(t, converted["audio_urls"].([]any)[0], wav)
	})
	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{"malformed-base64", map[string]any{"images_base64": []any{"not!base64"}}},
		{"mismatched-kind", map[string]any{"images_base64": []any{dataURL("image/png", wav)}}},
		{"mismatched-data-url", map[string]any{"images_base64": []any{dataURL("audio/wav", wav)}}},
		{"file-url", map[string]any{"image_urls": []any{"file:///private"}}},
		{"blob-url", map[string]any{"image_urls": []any{"blob:client-private"}}},
		{"credentials-in-url", map[string]any{"image_urls": []any{"https://user:pass@cdn.example/a.png"}}},
		{"unsupported-content", map[string]any{"images_base64": []any{dataURL("image/png", []byte("<html>wrong</html>"))}}},
		{"partial-rollback", map[string]any{"images_base64": []any{dataURL("image/png", png)}, "videos_base64": []any{"invalid"}}},
		{"decode-error-after-prefix", map[string]any{"images_base64": []any{dataURL("image/png", append(png, bytes.Repeat([]byte{0}, 1024)...)) + "!"}}},
		{"too-many-references", map[string]any{"image_urls": func() []any {
			values := make([]any, 65)
			for i := range values {
				values[i] = "https://cdn.example/a.png"
			}
			return values
		}()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CANVAS_ASSET_DIR", t.TempDir())
			_, err := NormalizeCanvasVideoReferences(context(), tc.body)
			require.Error(t, err)
			entries, err := os.ReadDir(CanvasAssetDirectory())
			require.NoError(t, err)
			assert.Empty(t, entries, "invalid requests leave no reference files")
		})
	}
	t.Run("ordinary-urls-do-not-require-public-storage", func(t *testing.T) {
		t.Setenv("CANVAS_ASSET_DIR", t.TempDir())
		system_setting.ServerAddress = "http://localhost:3000"
		defer func() { system_setting.ServerAddress = "https://branch.example/gateway" }()
		_, err := NormalizeCanvasVideoReferences(context(), map[string]any{"video_urls": []any{"https://cdn.example/a.mp4"}})
		require.NoError(t, err)
		_, err = NormalizeCanvasVideoReferences(context(), map[string]any{"images_base64": []any{dataURL("image/png", png)}})
		require.Error(t, err)
	})
	t.Run("storage-size-limit-and-cleanup", func(t *testing.T) {
		t.Setenv("CANVAS_ASSET_DIR", t.TempDir())
		_, err := StoreCanvasAsset(io.MultiReader(bytes.NewReader(png), io.LimitReader(zeroCanvasReader{}, CanvasAssetLimit)), "image")
		require.Error(t, err)
		entries, err := os.ReadDir(CanvasAssetDirectory())
		require.NoError(t, err)
		assert.Empty(t, entries)
	})
}

type zeroCanvasReader struct{}

func (zeroCanvasReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}
