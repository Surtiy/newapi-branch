package authz

const ResourceBranch = "branch"

var BranchSync = Permission{Resource: ResourceBranch, Action: "sync"}

func init() {
	RegisterResource(ResourceDefinition{
		Resource: ResourceBranch,
		LabelKey: "Branch Operations",
		Actions: []ActionDefinition{
			{
				Action:       "sync",
				LabelKey:     "Sync main site catalog",
				DescriptionKey: "Fetch and apply approved model, price, billing, and group updates from the main site.",
				DefaultRoles: []string{BuiltInRoleBranchOperator},
			},
		},
	})
}
