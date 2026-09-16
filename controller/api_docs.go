package controller

import (
	"crypto/sha256"
	_ "embed"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

//go:embed api_docs_default.json
var branchDocsDefault string

const branchDocsOption = "BranchApiDocsConfig"
var branchDocsMutex sync.Mutex

type branchDocsConfig struct {
	BaseURL string `json:"base_url"`
	Docs []dto.ApiDoc `json:"docs"`
}

func readBranchDocs() (branchDocsConfig, string, error) {
	common.OptionMapRWMutex.RLock()
	raw := common.OptionMap[branchDocsOption]
	common.OptionMapRWMutex.RUnlock()
	if raw == "" { raw = branchDocsDefault }
	var config branchDocsConfig
	err := common.UnmarshalJsonStr(raw, &config)
	return config, fmt.Sprintf("%x", sha256.Sum256([]byte(raw))), err
}

func validateBranchDocs(config *branchDocsConfig) error {
	config.BaseURL = strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if config.BaseURL != "" {
		parsed, err := url.Parse(config.BaseURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.Path != "" || strings.ContainsAny(config.BaseURL, "\r\n\t <>\"'`{}\\") {
			return errors.New("请输入完整的 http/https 域名，可带端口，不要包含 /v1、路径、密钥或查询参数")
		}
	}
	if config.Docs == nil || len(config.Docs) > 200 { return errors.New("文档列表无效") }
	seen := map[string]bool{}
	for _, doc := range config.Docs {
		if strings.TrimSpace(doc.ID) == "" || seen[doc.ID] || strings.TrimSpace(doc.Title) == "" || strings.TrimSpace(doc.Content) == "" { return errors.New("文档编号不能重复，标题和内容不能为空") }
		seen[doc.ID] = true
	}
	return nil
}

func GetApiDocs(c *gin.Context) {
	config, _, err := readBranchDocs()
	if err != nil { common.ApiErrorMsg(c, "接口文档配置读取失败"); return }
	c.Header("Cache-Control", "no-store")
	common.ApiSuccess(c, config)
}

func GetBranchDocsConfig(c *gin.Context) {
	config, version, err := readBranchDocs()
	if err != nil { common.ApiErrorMsg(c, "接口文档配置读取失败"); return }
	c.Header("Cache-Control", "no-store")
	common.ApiSuccess(c, gin.H{"config": config, "version": version})
}

func UpdateBranchDocsConfig(c *gin.Context) {
	var request struct { Config branchDocsConfig `json:"config"`; Version string `json:"version"` }
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4<<20)
	if c.ShouldBindJSON(&request) != nil { common.ApiErrorMsg(c, "文档配置格式错误或超过 4MB"); return }
	if err := validateBranchDocs(&request.Config); err != nil { common.ApiError(c, err); return }
	branchDocsMutex.Lock()
	defer branchDocsMutex.Unlock()
	_, version, err := readBranchDocs()
	if err != nil { common.ApiErrorMsg(c, "接口文档配置读取失败"); return }
	if version != request.Version { c.JSON(http.StatusConflict, gin.H{"success": false, "message": "文档已被修改，请刷新后重试"}); return }
	raw, err := common.Marshal(request.Config)
	if err != nil { common.ApiError(c, err); return }
	if err := model.UpdateOption(branchDocsOption, string(raw)); err != nil { common.ApiError(c, err); return }
	recordManageAudit(c, "branch.docs.update", map[string]any{"count": len(request.Config.Docs), "base_url": request.Config.BaseURL})
	common.ApiSuccess(c, gin.H{"config": request.Config, "version": fmt.Sprintf("%x", sha256.Sum256(raw))})
}
