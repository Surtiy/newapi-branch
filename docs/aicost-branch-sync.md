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

## Upstream API Keys and Directory Groups

Any upstream user's valid API key can be added. The branch does not require
an administrator's upstream key, an operator-approved key list, a local group
snapshot, SSH access to the main site, or deployment-specific key fingerprints.
Branch administrators still need the existing local channel-management
permission to add, replace, delete, or synchronize upstream credentials.

The branch requests the public read-only `/api/distributor/models` catalog and
`/v1/models` with the submitted key over HTTPS. Only published catalog entries
also returned in that key's model list contribute models and group ratios.
Authentication errors, redirects, malformed responses, and unavailable model
lists stop synchronization without falling back to the account-wide catalog
or pruning working routes. The upstream still enforces token status, expiry,
account status and applicable IP restrictions on `/v1/models`.

Groups are derived from the current filtered catalog on each successful fetch,
so new installations need no local verification files and source snapshots
refresh when upstream models or directory groups change. Existing operational
snapshot files, if left on disk by an old installation, are no longer read and
can be removed by that installation's operator. Upgrading the application and
restarting it is sufficient; no database migration or host-specific setup is
required for this change.

**Directory associations are not verified token bindings.** The current main
API does not expose the token's actual bound groups. A model can be visible in
several directory groups even when the token is bound to only one. In that
case all candidate directory groups remain visible; the branch does not pick
the first or cheapest group or claim to have identified an exact binding.
Review these candidates against the token's main-site configuration before
importing prices or enabling routes. The existing duplicate model/group
warnings across saved sources remain advisory. Exact automatic binding
identification needs explicit upstream token-scope metadata; this update does
not add that metadata or modify the main site.

Credentials are sent only to the configured integration's fixed HTTPS origin;
redirects are not followed. API responses and audit events keep keys masked or
omit them. Do not commit raw keys, SSH credentials or deployment environment
files when publishing the repository.

## Deploying on Another Host

There is no branch-machine IP, hostname, absolute workspace path, SSH key, or
operator-maintained token registry required by this integration. The browser
uses same-origin API requests. The video plugin takes the upstream URL and
credential from its channel context (`ctx.baseUrl` and `ctx.apiKey`). The catalog
integration targets `https://aicost.me` intentionally and does not send submitted
credentials to arbitrary origins.

For a public installation:

- Build this repository's source using its Dockerfile, for example
  `docker build -t new-api-branch:local .`, and use that image in your deployment.
  The inherited Compose examples reference the upstream `calciumion/new-api`
  image; pulling that image alone does not include this branch's changes.
- Set **Server Address** (`ServerAddress`) to your own public HTTPS URL in
  system settings. Set **Task Public Address** (`TaskPublicAddress`) when media
  uses a different public origin; otherwise artifact URLs use Server Address.
  The development default `http://localhost:3000` is not your public address.
  URL generation deliberately does not trust arbitrary request Host headers.
- Import and enable the video plugin as described below. Its database entry is
  separate from the application source and image.
- Supply your own database/cache credentials, persistent data volumes and
  session secrets. For HTTPS, configure secure cookies and exact trusted
  origins using the documented session settings. No credentials from another
  installation are needed.

Loopback health checks, development proxies, test fixtures and configurable
local-provider defaults are not deployment locks. They remain separate from
public URL settings. Upstream key validation, branch management permissions,
redirect rejection and SSRF protections are retained.

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
repeated multiplier updates, scaled price display, task-media previews, log
response parsing and API key multi-group editing. Type-check and build from
the same source before publishing:

```sh
cd web
bun run typecheck
bun run build
bun run test src/features/system-settings/__tests__/branch-catalog-sync.test.tsx src/features/system-settings/models/__tests__/group-pane.test.tsx src/features/pricing/lib/__tests__/scaled-billing-expr.test.ts src/features/pricing/__tests__/dynamic-price.test.ts src/features/pricing/__tests__/model-cards.test.tsx src/features/model-pricing/__tests__/editor-currency.test.tsx src/features/usage-logs/components/__tests__/task-media-preview.test.tsx src/features/usage-logs/components/__tests__/mobile-card.test.tsx src/features/usage-logs/components/__tests__/usage-facts.test.tsx src/features/keys/lib/__tests__/auto-group-form.test.ts src/features/keys/components/__tests__/api-key-listing.test.tsx --pool=threads --maxWorkers=1 --testTimeout=20000
```
