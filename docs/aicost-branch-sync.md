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
  The repository's Compose configuration builds context `.` with image
  `new-api-branch:local`; use `docker compose up -d --build` from the repository
  root. Pulling the upstream `calciumion/new-api` image alone does not include
  this branch's changes. The Docker build lints the embedded branch plugin.
- Set **Server Address** (`ServerAddress`) to your own public HTTPS URL in
  system settings. Set **Task Public Address** (`TaskPublicAddress`) when media
  uses a different public origin; otherwise artifact URLs use Server Address.
  The development default `http://localhost:3000` is not your public address.
  URL generation deliberately does not trust arbitrary request Host headers.
- The video plugin is embedded in the application binary and registered at
  startup. Fresh Docker installations need no manual plugin import or database
  plugin row. Existing administrator overrides and disabled settings are honored
  as described below.
- Supply your own database/cache credentials, persistent data volumes and
  session secrets. For HTTPS, configure secure cookies and exact trusted
  origins using the documented session settings. No credentials from another
  installation are needed.

Loopback health checks, development proxies, test fixtures and configurable
local-provider defaults are not deployment locks. They remain separate from
public URL settings. Upstream key validation, branch management permissions,
redirect rejection and SSRF protections are retained.

## Video Plugin

`plugins/tasks/aicost-branch/plugin.js` is the single source of the
`aicost-branch` task plugin. The existing `go:embed tasks` and `RegisterFactory`
startup path include it in the Go binary and Docker image automatically. There
is no separate root JavaScript copy, plugin download, manual import, database
seeding, or automatic database update required for a fresh installation.

The factory implements `openai_video`, declares `seconds` usage, and supports
both per-request and per-second billing expressions. Branch catalog imports
map public video model names to its `aicost-branch-video` routing alias; the
upstream still receives the public model requested by the client.

An active, enabled database override with key `aicost-branch` continues to take
precedence. Updating the image does not overwrite that source or its settings.
Removing the override through the existing management workflow restores the
bundled factory, unless it is disabled. Keep the plugin key unchanged for
existing channels and tasks. Older imported versions remain administrator-owned;
remove an override only when intentionally returning to the bundled version.

The global `TaskPluginEnabled` option / `TASK_PLUGIN_ENABLED` environment setting
and the `TaskPluginDisabledFactoryKeys` option remain effective. Disabling the
factory key hides the bundled plugin, not an independently enabled override;
turning off the global switch hides both. The application does not re-enable
an explicitly disabled plugin during startup or synchronization.

### Streaming video content (factory 1.0.2)

The branch does not download completed videos to disk or require an artifact
store. Clients request `GET|HEAD /v1/videos/{branchTaskId}/content`; the plugin
uses the persisted **upstream** task id to call the channel's
`/v1/videos/{upstreamTaskId}/content` with its upstream Bearer credential. The
host relays the response as a stream, preserving Range/206 and media headers.
The caller's branch API key is not forwarded upstream. Completion without a
`video_url` is valid and no longer remains pending until a polling failure.

The upstream must implement the authenticated content endpoint and return
video bytes, not a JSON wrapper. Poll-result URLs are not used for downloads:
they may expire, require credentials, or point to a different origin. Existing
same-origin validation, SSRF protection, task ownership checks and credentialed
cross-origin redirect rejection remain in force. An upstream that redirects
content to a different CDN origin must stream the content itself or use an
explicitly reviewed adapter; this update does not relax redirect policy.

This change only addresses video result retrieval. It does not add assets
upload, asynchronous image polling, video upscaling parameters, or optional
pricing/model-plaza endpoints. Existing administrator-owned plugin overrides
must be upgraded intentionally or removed to use the updated factory; building
a new image alone does not replace an override. No running service is updated
by a Git push.

Security review references: OWASP ASVS 5.0.0 (4.1.1 response content types,
12.3.1 encrypted outbound transport, 12.3.2 TLS certificate validation), and
the OWASP Authentication, Session Management, REST Security and SSRF Prevention
Cheat Sheets. This focused change retains the host transport and authorization
policies; use an HTTPS channel base URL in production. It is not a claim of
full-system ASVS compliance.

## Focused Regression Tests

Backend tests can be run from the repository root with:

```sh
go test -p 1 ./plugins -run '^TestBuiltIn' -count=1
go test -p 1 ./controller -run 'Test(AICostBranchVideoContent|TaskArtifactAuthorization|TaskArtifactAccess|ProxyTaskMedia|TaskMedia|CredentiallessTaskMedia|SelfTaskMedia)' -count=1
go test -p 1 ./controller ./model -run 'Test(Branch|RequestBranchCatalog|ScopeBranchCatalog|VideoBillingExpression)' -count=1
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
