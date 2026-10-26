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

### Streaming video content and completion checks (factory 1.0.3)

The branch does not download completed videos to disk or require an artifact
store. Clients request `GET|HEAD /v1/videos/{branchTaskId}/content`; the plugin
uses the persisted **upstream** task id to call the channel's
`/v1/videos/{upstreamTaskId}/content` with its upstream Bearer credential. The
host relays the response as a stream, preserving Range/206 and media headers.
The caller's branch API key is not forwarded upstream.

**Success requires both a completed upstream status and readable video bytes.**
The synchronous plugin parser returns a candidate completion; the host's
optional `buildCompletionCheckRequest` gate then samples the upstream content
using an authenticated GET with `Range: bytes=0-511` and an eight-second deadline.
Only HTTP 200/206 with a video or generic binary MIME type, a nonempty body and
a recognized video signature can complete the task. A `video_url` string or a
successful HEAD alone is not proof. JSON, HTML, empty streams, redirects and
temporary download errors leave the task in progress at 99%, without settling
success, consuming poll-failure retries or submitting another paid generation.
The existing outer timeout/refund remains in force. A later successful probe
allows normal settlement. Probing reads at most 512 bytes and never stores the
file; it does not validate the entire video or guarantee future URL availability.
This guard also runs for the host's legacy common-response decoder.

This version requires the updated host binary as well as the plugin. Importing
the new JavaScript alone into an older host does not install the completion gate.

The upstream must implement the authenticated content endpoint and return
video bytes, not a JSON wrapper. Poll-result URLs are not used for downloads:
they may expire, require credentials, or point to a different origin. Existing
same-origin validation, SSRF protection, task ownership checks and credentialed
cross-origin redirect rejection remain in force. An upstream that redirects
content to a different CDN origin must stream the content itself or use an
explicitly reviewed adapter; this update does not relax redirect policy.

That change only addressed video result retrieval; canvas additions in 1.0.4
are described below. Existing administrator-owned plugin overrides
must be upgraded intentionally or removed to use the updated factory; building
a new image alone does not replace an override. No running service is updated
by a Git push.

Security review references: OWASP ASVS 5.0.0 (4.1.1 response content types,
12.3.1 encrypted outbound transport, 12.3.2 TLS certificate validation), and
the OWASP Authentication, Session Management, REST Security and SSRF Prevention
Cheat Sheets. This focused change retains the host transport and authorization
policies; use an HTTPS channel base URL in production. It is not a claim of
full-system ASVS compliance.

### Canvas v0.4.98 compatibility (factory 1.0.4)

The integration document's sections 2–10 describe the current client. Its
section 1 is a future client configuration proposal, not additional gateway
routes. Use the branch's public HTTPS root URL and a **branch user API key**.
The main site, production prices and customer data are not changed by this
source update. A new image contains the host routes and embedded plugin together;
there is no separate upload service or manually installed adapter.

* Existing Chat Completions/SSE, synchronous images, multipart image edits
  (`image` / `image[]`), speech and Gemini-native routes remain in place.
  Image integration uses the document's **synchronous** branch: a final
  `data[].url` / `data[].b64_json` result. This release does **not** implement
  asynchronous image acceptance/persistence or
  `GET /v1/images/generations/{id}`. Configure an upstream offering synchronous
  images; an arbitrary upstream's 202 image task is not converted into a
  branch async task. No placeholder task IDs or premature success settlement
  are added.
* Ordinary video create/poll/content keep the public model name, complete
  prompt, ratio/resolution aliases, `client_task_id`, `image_urls`,
  `images_base64`, `video_urls`, `audio_urls` and `audios_base64`.
  Media support/count limits still depend on the configured upstream model.
  Video results are streamed, not saved locally. A completed upstream status
  **and readable video bytes** are still required for branch completion.
* A model name containing `超分` accepts the documented single `videos` URL,
  resolution `720p`/`1080p`/`2K`/`4K` (case-insensitive validation) and fps
  30/60/120/240 without inventing a prompt. Duration may be omitted **only**
  when the host confirms the local model uses the importer's simple fixed
  `tier("request", price)` tariff. For a second-priced or other tariff,
  provide the real source `seconds`/`duration` (greater than 0, at most 3600);
  missing duration fails validation before paid submission. The gateway does
  not remotely probe source video duration/size. The client's 128 MiB/3600s
  source limits also need enforcement by the configured upstream.
  `Idempotency-Key` is forwarded after printable-ASCII/255-character validation;
  this is **not** a new branch-wide idempotency or exactly-once billing store.
  A query/refresh must not resubmit a generation.

#### Reference upload, inside the same container

`POST /v1/assets` requires the normal Bearer API key and exactly one multipart
`file`. It returns `{"url":"https://branch.example/v1/assets/<random>.png"}`.
Configure **ServerAddress** to the branch's public HTTPS root first: URLs are
not built from an untrusted Host header, and localhost/private-IP addresses
are rejected. The reverse proxy must accept 129 MiB request bodies if the full
128 MiB file limit is needed.

This stores **input reference material**, not generated videos. Storage is
`canvas-assets/` under the process working directory (`/data/canvas-assets`
in the bundled Docker image, covered by the existing `/data` mount).
`CANVAS_ASSET_DIR` optionally selects a different dedicated persistent directory.
Uploads require an existing enabled user/key, use the existing upload limiter,
sniff allowed image/video/audio signatures, generate 128-bit random names,
reject HTML/SVG/unknown types and enforce a 128 MiB file ceiling and a five-minute
HTTP body read deadline. The single
writer's pool admits at most 512 files and reserves space within 2 GiB. Incomplete
uploads are removed. These limits are not a distributed multi-writer quota;
use one upload writer or a reviewed object-storage adapter for multiple nodes.

`GET|HEAD /v1/assets/{randomName}` supports Range/206 without a browser cookie
so providers can fetch references. These are **public capability URLs**: anyone
who receives the URL can read that material until it expires. Do not expose
the storage directory as a static directory listing. Files expire after seven
days; retrieval rejects expired files and the next upload removes expired
owned files. Keep the full lifetime longer than upstream queuing/generation.
No antivirus, full media-decoder validation, content moderation or cross-node
quota system is implied. Keep references private through an appropriate
object-storage integration if public capability URLs do not suit the deployment.

#### Optional catalog and model information

Both routes use **ordinary** `TokenAuth`, not administrator session auth:

* `GET /api/distributor/models` returns
  `{"success":true,"data":{"models":[...]}}`. The rows use **local branch
  pricing after synchronization/multiplier**, effective token groups and token
  model limits. Other groups and upstream keys are not returned. Simple imported
  video tariffs expose their actual scalar `model_price`, `quota_type:1` and
  `video_billing_unit:request|second`; custom expressions remain `tiered_expr`
  instead of being misrepresented as a fixed price. Group/user-group ratios
  are returned separately, not multiplied into the base price twice. These
  remain directory estimates, not a promise of an exact parameterized charge.
* `GET /api/usage/token/model-plaza?category=video` returns the same visible
  model scope, descriptions and tag arrays. It currently explicitly returns
  `has_success_rate:false` and omits `success_rate`; this branch has no token-scoped
  aggregation behind this compatibility endpoint. No fabricated 0%/100% and no
  other users' metrics are exposed. Window metadata is 120 minutes.

These extensions are optional and are not called as generation prerequisites.
Model capability advertising does not transform arbitrary provider protocols.

Security references additionally include the OWASP File Upload Cheat Sheet
(signature/size allowlists, generated names, non-webroot storage, authenticated
uploads and controlled public retrieval). Tests exercise ordinary token access,
missing/invalid keys, token group/model isolation, local tariff units, MIME
rejection, path traversal, upload cleanup/expiry and Range/HEAD. The public
reference URL and unavailable success-rate aggregation are explicit boundaries,
not a full-system ASVS claim. No database schema or migration is changed.

Validation on 2026-10-26: `node --test branch-plugin.test.mjs` passed 26 cases;
`go test -p 1 ./plugins ./relay/channel/task/jsplugin ./router
./setting/billing_setting -count=1` passed, as did focused controller/service
video and model-list regressions, image multipart/response regressions, a full
host build and `plugin lint`. A real local process served the new routes
(unauthenticated catalog/upload 401; unknown asset GET/HEAD 404).
`go test -p 1 ./controller -run '^TestCanvas' -count=1` passed on SQLite 3.50.4.
`CANVAS_TEST_SQL_DSN=<disposable-canvas_compat_test-DSN> go test -p 1
./controller -run '^TestCanvasCatalogOrdinaryTokenScopeAndBranchTariffs$'
-count=1 -v` passed on PostgreSQL 16.15 and MySQL 8.0.46 using temporary isolated
containers, then those containers/volumes were removed. This tests runtime
catalog/auth queries, not a new migration. MySQL uses the test fixture's
nullable TEXT token-group column: the existing production `Token.Groups`
`TEXT DEFAULT ''` tag fails MySQL fresh-table creation (error 1101), an existing
startup/migration limitation not changed or claimed fixed by this patch.

## Focused Regression Tests

Backend tests can be run from the repository root with:

```sh
go test -p 1 ./plugins -run '^TestBuiltIn' -count=1
go test -p 1 ./relay/channel/task/jsplugin -run TestBranchVideoCompletion -count=1
go test -p 1 ./service -run 'Test(CommonVideoCompletion|UpdateVideoSingleTaskPollClassification)' -count=1
go test -p 1 ./controller -run 'Test(AICostBranchVideoContent|TaskArtifactAuthorization|TaskArtifactAccess|ProxyTaskMedia|TaskMedia|CredentiallessTaskMedia|SelfTaskMedia)' -count=1
go test -p 1 ./controller ./model -run 'Test(Branch|RequestBranchCatalog|ScopeBranchCatalog|VideoBillingExpression)' -count=1
node --test branch-plugin.test.mjs
go test -p 1 ./controller -run '^TestCanvas' -count=1
go test -p 1 ./relay/channel/task/jsplugin -run '^TestBranchCanvasUpscaleBillingContext$' -count=1
go test -p 1 ./setting/billing_setting -run '^TestVideoCatalogTariff$' -count=1
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
