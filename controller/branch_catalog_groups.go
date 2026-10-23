package controller

import "github.com/QuantumNous/new-api/model"

// These are directory associations for the models returned by /v1/models,
// not proof of a token's actual group bindings. No deployment-local registry
// or operator approval is required to import a normal upstream API key.
func branchCatalogGroups(catalog *branchCatalog) ([]string, error) {
	groups := make([]string, 0, len(catalog.GroupRatio))
	for group := range catalog.GroupRatio {
		groups = append(groups, group)
	}
	if len(groups) == 0 {
		return groups, nil
	}
	return model.NormalizeBranchCatalogGroups(groups)
}
