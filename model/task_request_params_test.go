package model

import (
	"encoding/json"
	"testing"
)

func TestBuildTaskRequestParamsRedactsCredentials(t *testing.T) {
	got := BuildTaskRequestParams([]byte("{\"model\":\"video\",\"prompt\":\"hello\",\"seconds\":6,\"api_key\":\"secret\",\"headers\":{\"Authorization\":\"Bearer secret\"}}"), "application/json")
	var value map[string]any
	if err := json.Unmarshal(got, &value); err != nil { t.Fatal(err) }
	if _, ok := value["api_key"]; ok { t.Fatal("api key was retained") }
	if _, ok := value["headers"]; ok { t.Fatal("headers were retained") }
	if value["model"] != "video" { t.Fatalf("unexpected model: %#v", value["model"]) }
}

func TestBuildTaskRequestParamsSkipsMultipart(t *testing.T) {
	if got := BuildTaskRequestParams([]byte("{\"model\":\"video\"}"), "multipart/form-data"); got != nil { t.Fatal("multipart body should not be persisted") }
}
