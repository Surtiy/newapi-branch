package model

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/textproto"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildTaskRequestParamsRedactsCredentials(t *testing.T) {
	body := []byte(`{"model":"video","prompt":"hello","seconds":6,"face":false,"keyframes":[0,5],"monkey":"business","api_key":"KEY_CANARY","headers":{"Authorization":"HEADER_CANARY"},"nested":[{"accessToken":"TOKEN_CANARY","signature":"SIGN_CANARY","clientSecret":"SECRET_CANARY","password":"PASS_CANARY","prompt":"keep"}],"images":["https://user:USER_CANARY@cdn.example/image.png?key=KEY_CANARY&token=TOKEN_CANARY&X-Amz-Signature=SIGN_CANARY&width=512#token=FRAGMENT_CANARY"]}`)
	original := bytes.Clone(body)
	got := BuildTaskRequestParams(body, "application/json; charset=utf-8")
	var value map[string]any
	require.NoError(t, common.Unmarshal(got, &value))
	assert.Equal(t, original, body, "capturing must not mutate upstream request bytes")
	assert.Equal(t, "video", value["model"])
	assert.Equal(t, "hello", value["prompt"])
	assert.Equal(t, float64(6), value["seconds"])
	assert.Equal(t, false, value["face"])
	assert.Equal(t, []any{float64(0), float64(5)}, value["keyframes"])
	assert.Equal(t, "business", value["monkey"])
	assert.NotContains(t, value, "api_key")
	assert.NotContains(t, value, "headers")
	assert.Equal(t, []any{map[string]any{"prompt": "keep"}}, value["nested"])
	assert.Equal(t, []any{"https://cdn.example/image.png?width=512"}, value["images"])
	assert.NotContains(t, string(got), "CANARY")
}

func TestBuildTaskRequestParamsSanitizesEveryURLAndEmbeddedJSON(t *testing.T) {
	cases := []struct{ name, body string }{
		{"inline prompt", `{"prompt":"see https://cdn.example/a?api%5Fkey=CANARY&size=2 then /b?token=CANARY"}`},
		{"nested URL", `{"images":["https://cdn.example/a?redirect=https%3A%2F%2Fb.example%2Fx%3Fsignature%3DCANARY"]}`},
		{"malformed query", `{"images":["https://cdn.example/a?token=CANARY&bad=%zz"]}`},
		{"embedded JSON", `{"metadata":"{\"api_key\":\"CANARY\",\"prompt\":\"keep\"}"}`},
		{"inline file", `{"images":["data:image/png;base64,CANARY"]}`},
		{"URL as property", `{"https://cdn.example/a?token=CANARY":"value","model":"video"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := BuildTaskRequestParams([]byte(tc.body), "application/json")
			require.NotEmpty(t, got)
			assert.NotContains(t, string(got), "CANARY")
		})
	}
}

func TestBuildTaskRequestParamsMultipart(t *testing.T) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for _, field := range []struct{ key, value string }{
		{"model", "video"}, {"prompt", "hello"}, {"seconds", "6"},
		{"images", "https://cdn.example/a?token=CANARY&size=2"}, {"images", "https://cdn.example/b"},
		{"metadata", `{"face":true,"api_key":"CANARY","headers":{"Cookie":"CANARY"}}`},
		{"api_key", "CANARY"}, {"headers", "CANARY"},
	} {
		require.NoError(t, w.WriteField(field.key, field.value))
	}
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="face"; filename="face.png"`)
	h.Set("Content-Type", "image/png; secret=CANARY")
	h.Set("Authorization", "CANARY")
	part, err := w.CreatePart(h)
	require.NoError(t, err)
	_, err = io.WriteString(part, "FILE_BYTES_CANARY")
	require.NoError(t, err)
	require.NoError(t, w.Close())
	original := bytes.Clone(body.Bytes())
	got := BuildTaskRequestParams(body.Bytes(), w.FormDataContentType())
	var value map[string]any
	require.NoError(t, common.Unmarshal(got, &value))
	assert.Equal(t, original, body.Bytes())
	assert.Equal(t, "video", value["model"])
	assert.Equal(t, "hello", value["prompt"])
	assert.Equal(t, "6", value["seconds"])
	assert.Equal(t, []any{"https://cdn.example/a?size=2", "https://cdn.example/b"}, value["images"])
	assert.Equal(t, map[string]any{"face": true}, value["metadata"])
	assert.Equal(t, map[string]any{"filename": "face.png", "size": float64(len("FILE_BYTES_CANARY"))}, value["face"])
	assert.NotContains(t, string(got), "CANARY")
	assert.NotContains(t, value, "api_key")
	assert.NotContains(t, value, "headers")
}

func TestBuildTaskRequestParamsBoundsAndInvalidInput(t *testing.T) {
	for _, tc := range []struct{ name, body, contentType string }{
		{"empty historical snapshot", "", "application/json"},
		{"null historical snapshot", "null", "application/json"},
		{"invalid JSON", `{"prompt":`, "application/json"},
		{"trailing JSON", `{} {}`, "application/json"},
		{"non-object", `"text"`, "application/json"},
		{"depth", strings.Repeat(`{"a":`, 40) + `0` + strings.Repeat("}", 40), "application/json"},
		{"wide array", `{"images":[` + strings.Repeat(`0,`, 4096) + `0]}`, "application/json"},
		{"input bytes", `{"prompt":"` + strings.Repeat("x", 1024*1024) + `"}`, "application/json"},
		{"output bytes", `{"images":["` + strings.Repeat("x", 4096) + `","` + strings.Repeat("y", 4096) + `","` + strings.Repeat("z", 4096) + `","` + strings.Repeat("w", 4096) + `"]}`, "application/json"},
		{"missing boundary", "body", "multipart/form-data"},
		{"broken multipart", "--fixture\r\n", "multipart/form-data; boundary=fixture"},
		{"empty multipart", "--fixture--\r\n", "multipart/form-data; boundary=fixture"},
		{"too many parts", strings.Repeat("--fixture\r\nContent-Disposition: form-data; name=face\r\n\r\nx\r\n", 100) + "--fixture--\r\n", "multipart/form-data; boundary=fixture"},
		{"oversized text", "--fixture\r\nContent-Disposition: form-data; name=prompt\r\n\r\n" + strings.Repeat("x", 256*1024+1) + "\r\n--fixture--\r\n", "multipart/form-data; boundary=fixture"},
		{"unsupported content type", `{}`, "text/plain"},
		{"invalid media type", `{}`, "application/json-invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) { assert.Nil(t, BuildTaskRequestParams([]byte(tc.body), tc.contentType)) })
	}
}

func TestBuildTaskRequestParamsTruncatesUTF8AndIsIdempotent(t *testing.T) {
	body, err := common.Marshal(map[string]any{"prompt": strings.Repeat("中", 2000), "images": []string{"https://cdn.example/a?token=CANARY&size=2"}})
	require.NoError(t, err)
	got := BuildTaskRequestParams(body, "application/json")
	var value map[string]any
	require.NoError(t, common.Unmarshal(got, &value))
	prompt, ok := value["prompt"].(string)
	require.True(t, ok)
	assert.NotContains(t, prompt, "\ufffd")
	assert.True(t, strings.HasSuffix(prompt, "…"))
	assert.LessOrEqual(t, len(got), 16*1024)
	assert.JSONEq(t, string(got), string(BuildTaskRequestParams(got, "application/json")))
}

func TestBuildTaskRequestParamsPreservesBusinessFields(t *testing.T) {
	body := []byte(`{"model":"video","prompt":"[wide shot] {cinematic}","images":["https://cdn.example/reference.png?size=512"],"face":{"enabled":false,"strength":0},"seconds":0,"max_tokens":0,"max_output_tokens":512,"maxTokens":8,"prompt_tokens":10,"token_count":0,"tokenizer":"business","business":{"enabled":false,"count":0},"seed":9223372036854775807,"access_token":"CANARY","refreshToken":"CANARY","token":"CANARY"}`)
	got := BuildTaskRequestParams(body, "application/json")
	require.NotEmpty(t, got)
	assert.JSONEq(t, `{"model":"video","prompt":"[wide shot] {cinematic}","images":["https://cdn.example/reference.png?size=512"],"face":{"enabled":false,"strength":0},"seconds":0,"max_tokens":0,"max_output_tokens":512,"maxTokens":8,"prompt_tokens":10,"token_count":0,"tokenizer":"business","business":{"enabled":false,"count":0},"seed":9223372036854775807}`, string(got))
	assert.Contains(t, string(got), `"seed":9223372036854775807`)
	assert.JSONEq(t, string(got), string(BuildTaskRequestParams(got, "application/json")))
}

func TestBuildTaskRequestParamsReaderBudget(t *testing.T) {
	reader := strings.NewReader(`{"prompt":"` + strings.Repeat("x", 1024*1024) + `"}`)
	size := reader.Len()
	assert.Nil(t, BuildTaskRequestParamsFromReader(reader, "application/json"))
	assert.LessOrEqual(t, size-reader.Len(), 256*1024+1)

	reader = strings.NewReader("--fixture\r\nContent-Disposition: form-data; name=face; filename=face.png\r\n\r\n" + strings.Repeat("x", 9*1024*1024) + "\r\n--fixture--\r\n")
	size = reader.Len()
	assert.Nil(t, BuildTaskRequestParamsFromReader(reader, "multipart/form-data; boundary=fixture"))
	assert.LessOrEqual(t, size-reader.Len(), 8*1024*1024+1)
}

func TestTaskRequestParamsPreservesOriginalPrompt(t *testing.T) {
	for _, prompt := range []string{
		"@角色A #普通话 请保持原始文本",
		"@角色A/#普通话",
		"角色A@角色B，#普通话对白，为什么?保持100%原样",
		"联系 artist@example.com，场景 #1；镜头A/B",
		"[wide shot] {cinematic} @角色A\n#普通话",
	} {
		body, err := common.Marshal(map[string]any{"prompt": prompt})
		require.NoError(t, err)
		var snapshot map[string]any
		require.NoError(t, common.Unmarshal(BuildTaskRequestParams(body, "application/json"), &snapshot))
		assert.Equal(t, prompt, snapshot["prompt"])
	}
	body := []byte(`{"prompt":"@角色A #普通话 https://cdn.example/a?token=CANARY&size=2 /b?signature=CANARY user:CANARY@cdn.example/c?key=CANARY"}`)
	var snapshot map[string]any
	require.NoError(t, common.Unmarshal(BuildTaskRequestParams(body, "application/json"), &snapshot))
	assert.Equal(t, "@角色A #普通话 https://cdn.example/a?size=2 /b cdn.example/c", snapshot["prompt"])
}

func TestTaskRequestParamsDatabaseRoundTrip(t *testing.T) {
	setupBranchTestDB(t) // SQLite by default; BRANCH_TEST_POSTGRES / MYSQL select real engines.
	require.NoError(t, DB.AutoMigrate(&Task{}))
	jsonSnapshot := BuildTaskRequestParams([]byte(`{"model":"video","prompt":"@角色A #普通话","seconds":0,"face":{"enabled":false},"max_tokens":0,"seed":9223372036854775807,"api_key":"CANARY","images":["https://cdn.example/a?signature=CANARY&size=2"]}`), "application/json")
	require.NotEmpty(t, jsonSnapshot)
	var multipartBody bytes.Buffer
	w := multipart.NewWriter(&multipartBody)
	require.NoError(t, w.WriteField("prompt", "@角色A #普通话"))
	require.NoError(t, w.WriteField("max_tokens", "0"))
	file, err := w.CreateFormFile("face", "face.png")
	require.NoError(t, err)
	_, err = io.WriteString(file, "FILE_BYTES_CANARY")
	require.NoError(t, err)
	require.NoError(t, w.Close())
	multipartSnapshot := BuildTaskRequestParams(multipartBody.Bytes(), w.FormDataContentType())
	require.NotEmpty(t, multipartSnapshot)
	for _, tc := range []struct {
		name     string
		params   []byte
		expected string
	}{
		{"json", jsonSnapshot, string(jsonSnapshot)},
		{"multipart", multipartSnapshot, string(multipartSnapshot)},
		{"legacy-unsanitized", []byte(`{"prompt":"@角色A #普通话","api_key":"CANARY","headers":{"Cookie":"CANARY"},"images":["https://cdn.example/a?token=CANARY&size=2"]}`), `{"prompt":"@角色A #普通话","images":["https://cdn.example/a?size=2"]}`},
		{"historical-empty", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task := Task{TaskID: GenerateTaskID(), PrivateData: TaskPrivateData{RequestParams: tc.params}}
			task.SetData(map[string]any{"prompt": "upstream response must not become request history"})
			require.NoError(t, DB.Create(&task).Error)
			t.Cleanup(func() { assert.NoError(t, DB.Delete(&Task{}, task.ID).Error) })
			var stored Task
			require.NoError(t, DB.First(&stored, task.ID).Error)
			if len(tc.params) == 0 {
				assert.Empty(t, stored.PrivateData.RequestParams)
				assert.Nil(t, BuildTaskRequestParams(stored.PrivateData.RequestParams, "application/json"))
				return
			}
			// JSON columns may normalize whitespace/key order, but not parameter values.
			assert.JSONEq(t, string(tc.params), string(stored.PrivateData.RequestParams))
			before := bytes.Clone(stored.PrivateData.RequestParams)
			safe := BuildTaskRequestParams(stored.PrivateData.RequestParams, "application/json")
			assert.JSONEq(t, tc.expected, string(safe))
			assert.Equal(t, before, []byte(stored.PrivateData.RequestParams))
			encoded, err := common.Marshal(map[string]any{"request_params": safe})
			require.NoError(t, err)
			assert.NotContains(t, string(encoded), "CANARY")
			assert.NotContains(t, string(encoded), "upstream response")
			var output map[string]map[string]any
			require.NoError(t, common.Unmarshal(encoded, &output))
			assert.Equal(t, "@角色A #普通话", output["request_params"]["prompt"])
			if tc.name == "json" {
				assert.Contains(t, string(encoded), `"seed":9223372036854775807`)
				assert.Equal(t, float64(0), output["request_params"]["max_tokens"])
				assert.Equal(t, map[string]any{"enabled": false}, output["request_params"]["face"])
			} else if tc.name == "multipart" {
				assert.Equal(t, map[string]any{"filename": "face.png", "size": float64(len("FILE_BYTES_CANARY"))}, output["request_params"]["face"])
			}
		})
	}
}
