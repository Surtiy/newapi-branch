package model

import (
 "testing"
 "github.com/QuantumNous/new-api/common"
 "github.com/stretchr/testify/require"
)

func TestBranchGroupsLifecycle(t *testing.T) {
 // Uses the same database matrix setup as the existing import lifecycle test.
 setupBranchTestDB(t)
 base:="https://aicost.me"
 channel:=Channel{Name:"group-import",Type:1,Key:"test-only",Group:"default",Models:"group-model",Status:1,BaseURL:&base}
 require.NoError(t,DB.Create(&channel).Error)
 require.NoError(t,UpdateOption("GroupRatio",`{"default":1,"branch-existing":0.8}`))
 require.NoError(t,UpdateOption("UserUsableGroups",`{"default":"default","branch-existing":"existing"}`))
 build:=func() ([]BranchModelImport,[]BranchGroupSync) {
  snapshot,err:=GetModelPricingSnapshot([]string{"group-model"});require.NoError(t,err)
  groups,err:=GetBranchGroupStates([]string{"branch-existing","branch-new"});require.NoError(t,err)
  imports:=[]BranchModelImport{{Change:ModelPricingChange{ModelName:"group-model",ExpectedVersion:snapshot.Entries[0].Version,Pricing:PricingValues{"ModelPrice":2.0}},UpdatePrice:true,Metadata:Model{ModelName:"group-model",Endpoints:`["image-generation"]`},Groups:[]string{"branch-existing","branch-new"}}}
  sync:=[]BranchGroupSync{{Name:"branch-existing",Ratio:0.11,ExpectedVersion:groups["branch-existing"].Version},{Name:"branch-new",Ratio:0.45,ExpectedVersion:groups["branch-new"].Version}}
  return imports,sync
 }
 imports,groups:=build()
 require.NoError(t,ImportBranchModels(&channel,imports,groups...))
 var imported []Channel
 require.NoError(t,DB.Where("tag LIKE ?","branch-group-%").Find(&imported).Error)
 require.Len(t, imported, 2)
 for _, row := range imported { require.Equal(t, row.Group, row.Name) }
 states,err:=GetBranchGroupStates([]string{"branch-existing","branch-new"});require.NoError(t,err)
 require.Equal(t,0.8,*states["branch-existing"].Ratio)
 require.Equal(t,0.45,*states["branch-new"].Ratio)
 require.NoError(t,DB.First(&channel,channel.Id).Error)
 require.Empty(t,channel.Models,"selected models must leave the catch-all group")
 for _,name:=range []string{"branch-existing","branch-new"} {
  var count int64
  require.NoError(t,DB.Model(&Ability{}).Where(commonGroupCol+" = ? AND model = ?",name,"group-model").Count(&count).Error)
  require.EqualValues(t,1,count)
 }
 imports,groups=build();groups[0].UpdateRatio=true
 require.NoError(t,ImportBranchModels(&channel,imports,groups...))
 states,err=GetBranchGroupStates([]string{"branch-existing"});require.NoError(t,err)
 require.Equal(t,0.11,*states["branch-existing"].Ratio)
 imports,groups=build();groups[0].ExpectedVersion="stale";groups[1].UpdateRatio=true;groups[1].Ratio=99
 require.Error(t,ImportBranchModels(&channel,imports,groups...))
 states,err=GetBranchGroupStates([]string{"branch-new"});require.NoError(t,err)
 require.Equal(t,0.45,*states["branch-new"].Ratio,"conflict rolls back all group mutations")
 raw,_:=common.Marshal(states);require.NotEmpty(t,raw)
}
