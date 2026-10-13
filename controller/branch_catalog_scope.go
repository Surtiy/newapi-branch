package controller

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

// The main catalog describes account visibility, not token authorization. This
// operator-verified snapshot contains only key fingerprints, never credentials.
func verifiedBranchTokenGroups(key string) ([]string, error) {
	path := os.Getenv("BRANCH_TOKEN_BINDINGS_FILE")
	if path == "" {
		path = "/data/branch-token-bindings.json"
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("主站令牌绑定分组尚未核验，未保存或同步；模型目录关联分组不能作为令牌绑定依据")
	}
	defer file.Close()
	var snapshot struct {
		Version int `json:"version"`
		Tokens  map[string]struct {
			Groups     []string `json:"groups"`
			VerifiedAt int64    `json:"verified_at"`
		} `json:"tokens"`
	}
	if common.DecodeJson(io.LimitReader(file, 1<<20), &snapshot) != nil || snapshot.Version != 1 {
		return nil, errors.New("令牌分组核验记录无效，未保存或同步")
	}
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(key)))
	binding, ok := snapshot.Tokens[fingerprint]
	if !ok || binding.VerifiedAt <= 0 || binding.Groups == nil {
		return nil, errors.New("此主站令牌绑定分组尚未核验，未保存或同步；请先核验真实绑定，不能从同名模型推断分组")
	}
	if len(binding.Groups) == 0 {
		return []string{}, nil
	}
	groups, err := model.NormalizeBranchCatalogGroups(binding.Groups)
	if err != nil {
		return nil, errors.New("令牌分组核验记录无效，未保存或同步")
	}
	return groups, nil
}
