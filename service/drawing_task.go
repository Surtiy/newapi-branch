package service

import (
	"context"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	hosttypes "github.com/QuantumNous/new-api/types"
)

// Task artifacts are already private and durable. Project their identities,
// never provider credentials, raw Task.Data, or video/audio outputs.
func RecordTaskDrawing(ctx context.Context, task *model.Task, adaptor any) {
	if task == nil || task.Status != model.TaskStatusSuccess {
		return
	}
	provider, ok := adaptor.(interface {
		ListArtifacts(*model.Task) ([]hosttypes.TaskArtifact, error)
	})
	if !ok {
		return
	}
	artifacts, err := provider.ListArtifacts(task)
	if err != nil {
		logger.LogWarn(ctx, "drawing task artifact projection failed: "+task.TaskID)
		return
	}
	properties := DrawingLogProperties{Kind: "relay_image", ModelName: task.Properties.OriginModelName, Group: task.Group}
	for _, artifact := range artifacts {
		if artifact.Type == "image" && artifact.Key != "" && len(properties.Images) < 128 {
			properties.Images = append(properties.Images, DrawingLogImage{TaskID: task.TaskID, ArtifactKey: artifact.Key, MimeType: artifact.MimeType})
		}
	}
	if len(properties.Images) == 0 {
		return
	}
	if execution := task.PrivateData.Execution; execution != nil {
		properties.RequestID, properties.RequestPath = execution.RequestID, execution.RequestPath
	}
	// Each task is a distinct durable generation, even when multiple tasks share
	// one outer request ID. Repeated polling projects the same row idempotently.
	if properties.RequestID == "" {
		properties.RequestID = task.TaskID
	}
	if len(task.PrivateData.RequestParams) > 0 {
		_ = common.Unmarshal(task.PrivateData.RequestParams, &properties.RequestParams)
	}
	if properties.RequestParams == nil {
		properties.RequestParams = map[string]any{}
	}
	properties.RequestParams["model"] = properties.ModelName
	encoded, err := common.Marshal(properties)
	if err != nil {
		return
	}
	finish := task.FinishTime
	if finish == 0 {
		finish = time.Now().Unix()
	}
	action := "IMAGINE"
	if strings.Contains(properties.RequestPath, "/edits") {
		action = "EDITS"
	}
	record := model.Midjourney{UserId: task.UserId, MjId: "img_" + task.TaskID, ChannelId: task.ChannelId,
		Code: 1, Action: action, Status: "SUCCESS", Progress: "100%", Prompt: task.Properties.Input,
		SubmitTime: task.SubmitTime * 1000, StartTime: task.StartTime * 1000, FinishTime: finish * 1000,
		Quota: task.Quota, Properties: string(encoded)}
	if err := model.SaveDrawingLog(&record); err != nil {
		logger.LogWarn(ctx, "drawing task history persistence failed: "+task.TaskID)
	}
}
