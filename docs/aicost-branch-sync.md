# AICost Branch Sync

This document describes the branch-specific integration in New API. It does not
require changing the main AICost service.

## Changes In This Update

- Manage multiple upstream API keys using an add/replace dialog and masked key
  previews. List all source groups alongside a combined model table.
- Sort the combined table by model name and paginate by distinct names. All
  sources for a model stay on the same page.
- Allow identical model names in different groups. Warn about overlapping
  model/group bindings and identify their masked keys and source IDs.
- Reconcile removed upstream models and groups only after a complete, valid
  catalog response. Retain retired channel credentials for existing video tasks.
- Calculate each synchronization from the upstream price and the current
  multiplier, including an explicit multiplier of 1. Do not compound previous
  synchronization multipliers.
- Parse uniformly scaled billing expressions into their editable pricing fields
  and display prices consistently in the model catalog.
- Honor outer upstream task status, progress, result URLs and actual duration in
  the video plugin. Preview video artifacts directly in task details.

## Verified Token Groups

The upstream distributor catalog can describe account-visible groups that are
not actually bound to a particular token. Its group list is not authorization
evidence, even when a model name matches.

The backend reads an operator-verified snapshot from
`BRANCH_TOKEN_BINDINGS_FILE`, defaulting to `/data/branch-token-bindings.json`.
Each entry is indexed by the lowercase SHA-256 hex digest of the exact upstream
API key. No raw keys belong in this file.

The JSON schema is:

- `version`: the integer `1`.
- `tokens`: an object keyed by token fingerprints.
- `tokens[<fingerprint>].groups`: the verified group-name array. An empty array
  means the token has no bound groups.
- `tokens[<fingerprint>].verified_at`: a positive Unix timestamp in seconds.

Keep this file server-local and readable by the service. Do not commit the
snapshot, API keys, SSH credentials or deployment environment files. Refresh
the snapshot after token/group bindings change on the main site; the current
implementation does not dynamically verify those bindings through an upstream
API. Unknown fingerprints and invalid or missing snapshots fail closed without
saving a new source or reconciling its routes.

## Video Plugin

`branch-plugin.js` is the `aicost-branch` task-plugin source. Updating the Go
application alone does not update a previously imported plugin in the database.
Import/update and enable this plugin through the existing task-plugin management
workflow as part of deployment. Keep the plugin key unchanged so existing
channel configuration continues to resolve it.

## Focused Regression Tests

Backend tests can be run from the repository root with:

```sh
go test ./controller ./model -run 'Test(Branch|RequestBranchCatalog|ScopeBranchCatalog|VideoBillingExpression)' -count=1
node --test branch-plugin.test.mjs
```

The model tests default to an isolated temporary SQLite database. Set
`BRANCH_TEST_POSTGRES` or `BRANCH_TEST_MYSQL` to an isolated disposable database
DSN to exercise those engines. Never use a production database for these tests.

The corresponding frontend suites cover catalog pagination, source management,
repeated multiplier updates, scaled price display and task-media previews:

```sh
cd web
bun run test src/features/system-settings/__tests__/branch-catalog-sync.test.tsx src/features/system-settings/models/__tests__/group-pane.test.tsx src/features/pricing/lib/__tests__/scaled-billing-expr.test.ts src/features/pricing/__tests__/dynamic-price.test.ts src/features/pricing/__tests__/model-cards.test.tsx src/features/model-pricing/__tests__/editor-currency.test.tsx src/features/usage-logs/components/__tests__/task-media-preview.test.tsx --maxWorkers=1 --testTimeout=20000
```
