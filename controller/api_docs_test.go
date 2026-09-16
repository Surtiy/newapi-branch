package controller

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestBranchDocsURLValidation(t *testing.T) {
	for _, value := range []string{"", "http://198.51.100.20", "https://api.example.com/", "https://api.example.com:8443"} {
		config := branchDocsConfig{BaseURL: value, Docs: []dto.ApiDoc{}}
		require.NoError(t, validateBranchDocs(&config), value)
	}
	for _, value := range []string{"javascript:alert(1)", "https://user:secret@host", "https://host/v1", "https://host?key=secret", "https://host#fragment", "//host", "https://host\nmalicious"} {
		config := branchDocsConfig{BaseURL: value, Docs: []dto.ApiDoc{}}
		require.Error(t, validateBranchDocs(&config), value)
	}
}

func TestBranchDocsDefaultAndExplicitEmpty(t *testing.T) {
	common.OptionMapRWMutex.Lock()
	before := common.OptionMap
	common.OptionMap = map[string]string{}
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() { common.OptionMapRWMutex.Lock(); common.OptionMap = before; common.OptionMapRWMutex.Unlock() })
	config, _, err := readBranchDocs()
	require.NoError(t, err)
	require.Len(t, config.Docs, 6)
	require.NoError(t, validateBranchDocs(&config))
	for _, doc := range config.Docs { require.NotContains(t, doc.Content, "aicost.me"); require.Contains(t, doc.Content, "{{BASE_URL}}") }
	common.OptionMapRWMutex.Lock()
	common.OptionMap[branchDocsOption] = `{"base_url":"https://docs.example.com","docs":[]}`
	common.OptionMapRWMutex.Unlock()
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	GetApiDocs(context)
	require.Contains(t, response.Body.String(), `"docs":[]`)
	require.Contains(t, response.Body.String(), "https://docs.example.com")
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
}
