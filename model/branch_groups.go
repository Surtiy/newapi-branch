package model

import (
 "crypto/sha256"
 "errors"
 "fmt"
 "math"
 "slices"
 "strings"

 "github.com/QuantumNous/new-api/common"
 "github.com/QuantumNous/new-api/constant"
 "github.com/QuantumNous/new-api/setting"
 "github.com/QuantumNous/new-api/setting/ratio_setting"
 "gorm.io/gorm"
 "gorm.io/gorm/clause"
)

type BranchGroupState struct {
 Name string `json:"name"`
 Ratio *float64 `json:"ratio"`
 Version string `json:"version"`
}

type BranchGroupSync struct {
 Name string `json:"name"`
 Ratio float64 `json:"ratio"`
 ExpectedVersion string `json:"expected_version"`
 UpdateRatio bool `json:"update_ratio"`
}

func branchGroupState(name string, ratios map[string]float64, usable map[string]string) BranchGroupState {
 state:=BranchGroupState{Name:name}
 ratio,exists:=ratios[name]
 if exists {state.Ratio=&ratio}
 label,selectable:=usable[name]
 raw,_:=common.Marshal([]any{exists,ratio,selectable,label})
 state.Version=fmt.Sprintf("%x",sha256.Sum256(raw))
 return state
}

func readBranchGroupOptions(db *gorm.DB) (map[string]float64,map[string]string,error) {
 ratios:=ratio_setting.GetGroupRatioCopy()
 usable:=setting.GetUserUsableGroupsCopy()
 var options []Option
 if err:=db.Where(commonKeyCol+" IN ?",[]string{"GroupRatio","UserUsableGroups"}).Find(&options).Error;err!=nil{return nil,nil,err}
 for _,option:=range options {
  if option.Key=="GroupRatio" {ratios=map[string]float64{};if err:=common.UnmarshalJsonStr(option.Value,&ratios);err!=nil{return nil,nil,err}}
  if option.Key=="UserUsableGroups" {usable=map[string]string{};if err:=common.UnmarshalJsonStr(option.Value,&usable);err!=nil{return nil,nil,err}}
 }
 return ratios,usable,nil
}

func GetBranchGroupStates(names []string) (map[string]BranchGroupState,error) {
 ratios,usable,err:=readBranchGroupOptions(DB)
 if err!=nil{return nil,err}
 result:=map[string]BranchGroupState{}
 for _,name:=range names {result[name]=branchGroupState(name,ratios,usable)}
 return result,nil
}

func importBranchGroupOptions(tx *gorm.DB, groups []BranchGroupSync, imports []BranchModelImport) (map[string]string,error) {
 defaults:=map[string]string{"GroupRatio":ratio_setting.GroupRatio2JSONString(),"UserUsableGroups":setting.UserUsableGroups2JSONString()}
 for _,key:=range []string{"GroupRatio","UserUsableGroups"} {
  row:=Option{Key:key,Value:defaults[key]}
  if err:=tx.Clauses(clause.OnConflict{DoNothing:true}).Create(&row).Error;err!=nil{return nil,err}
 }
 ratios,usable,err:=readBranchGroupOptions(lockForUpdate(tx))
 if err!=nil{return nil,err}
 selected:=map[string]bool{}
 for _,group:=range groups {
  if group.Name=="" || group.Name=="auto" || strings.ContainsAny(group.Name,",\r\n") || len([]rune(group.Name))>64 || selected[group.Name] || math.IsNaN(group.Ratio) || math.IsInf(group.Ratio,0) || group.Ratio<0 {return nil,errors.New("分组配置无效")}
  if branchGroupState(group.Name,ratios,usable).Version!=group.ExpectedVersion {return nil,fmt.Errorf("分组 %s 的倍率或可用状态已变化，请重新拉取",group.Name)}
  if _,exists:=ratios[group.Name];!exists || group.UpdateRatio {ratios[group.Name]=group.Ratio}
  if _,exists:=usable[group.Name];!exists {usable[group.Name]=group.Name}
  selected[group.Name]=true
 }
 for _,item:=range imports {
  if len(item.Groups)==0 {return nil,fmt.Errorf("请选择 %s 的分组",item.Change.ModelName)}
  for _,name:=range item.Groups {if !selected[name] {return nil,errors.New("所选分组缺少确认配置")}}
 }
 encoded:=map[string]string{}
 for key,value:=range map[string]any{"GroupRatio":ratios,"UserUsableGroups":usable} {
  raw,err:=common.Marshal(value);if err!=nil{return nil,err}
  if err:=tx.Model(&Option{}).Where(commonKeyCol+" = ?",key).Update("value",string(raw)).Error;err!=nil{return nil,err}
  encoded[key]=string(raw)
 }
 return encoded,nil
}

func BranchGroupChannelTag(parentID int, group string, video bool) string {
 digest:=sha256.Sum256([]byte(group))
 return fmt.Sprintf("branch-group-%d-%x-%t",parentID,digest[:12],video)
}

func importBranchGroupChannels(tx *gorm.DB, parent *Channel, item BranchModelImport) error {
 for _,group:=range slices.Compact(slices.Sorted(slices.Values(item.Groups))) {
  tag:=BranchGroupChannelTag(parent.Id,group,item.Video)
  var channel Channel
  err:=tx.Where("tag = ?",tag).First(&channel).Error
  kind:=1
  if item.Video {kind=constant.ChannelTypeTaskPlugin}
  if errors.Is(err,gorm.ErrRecordNotFound) {
   channel=Channel{Name:group,Type:kind,Status:parent.Status,Key:parent.Key,BaseURL:parent.BaseURL,Group:group,Tag:&tag,CreatedTime:common.GetTimestamp()}
   if item.Video {setting:=`{"task_plugin_key":"aicost-branch"}`;channel.Setting=&setting}
   if err:=tx.Create(&channel).Error;err!=nil{return err}
  } else if err!=nil{return err}
  if channel.Type!=kind || channel.Group!=group || (item.Video && channel.GetSetting().TaskPluginKey!="aicost-branch") {return errors.New("已同步渠道配置发生变化，请先核对渠道")}
  if channel.Name!=group {if err:=tx.Model(&channel).Update("name",group).Error;err!=nil{return err};channel.Name=group}
  names:=channel.GetModels()
  if !slices.Contains(names,item.Change.ModelName) {names=append(names,item.Change.ModelName)}
  names=slices.DeleteFunc(names,func(name string)bool{return name==""});slices.Sort(names)
  channel.Models=strings.Join(names,",")
  updates:=map[string]any{"models":channel.Models,"key":parent.Key,"base_url":parent.BaseURL}
  if item.Video {
   mapping:=map[string]string{}
   if raw:=channel.GetModelMapping();raw!="" {if err:=common.UnmarshalJsonStr(raw,&mapping);err!=nil{return err}}
   mapping[item.Change.ModelName]="aicost-branch-video"
   raw,err:=common.Marshal(mapping);if err!=nil{return err};updates["model_mapping"]=string(raw)
  }
  if err:=tx.Model(&channel).Updates(updates).Error;err!=nil{return err}
  if err:=channel.UpdateAbilities(tx);err!=nil{return err}
 }
 // Migrate this selected model out of the original catch-all video channel.
 if item.Video {
  var legacy Channel
  err:=tx.Where("tag = ?",fmt.Sprintf("branch-video-%d",parent.Id)).First(&legacy).Error
  if err!=nil && !errors.Is(err,gorm.ErrRecordNotFound){return err}
  if err==nil {
   names:=slices.DeleteFunc(legacy.GetModels(),func(name string)bool{return name==item.Change.ModelName || name==""})
   legacy.Models=strings.Join(names,",")
   if err:=tx.Model(&legacy).Update("models",legacy.Models).Error;err!=nil{return err}
   if legacy.Models=="" {return tx.Where("channel_id = ?",legacy.Id).Delete(&Ability{}).Error}
   return legacy.UpdateAbilities(tx)
  }
 }
 return nil
}
