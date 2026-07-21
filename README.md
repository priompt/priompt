# Priompt

Priompt is a self-hostable, language-agnostic server for versioning and serving
prompts to AI agents over a network — the way a Git server serves repositories.
You run the server, your prompts live in your infrastructure, and your agents
fetch them by URI over gRPC.

It is a single Go binary that stores, validates, serves, versions, caches, and
distributes prompts, plus a Python and a JavaScript client, and `promptctl`, a
git-backed authoring CLI with a semantic diff.

Two pieces are new here, and the rest of the system exists to serve them:

- **The Semantic Propagation Diff** — a diff that measures *how far an edit's
  meaning shift ripples* through a prompt, separating a safe local tweak from a
  structural rewrite. A text diff cannot express this. See
  [Semantic Propagation Diff](#semantic-propagation-diff).
- **Change distribution to agents** — prompts are not static files an agent reads
  once; they are live, versioned resources, and the server pushes a notification
  to every subscribed agent the instant one changes. See
  [Pub/sub distribution](#pubsub-distribution).

Everything else — gRPC transport, the commit DAG, caching, auth — is in service
of getting those two capabilities to production safely.

## Contents

- [Overview](#overview)
- [Installation](#installation)
- [Quick start (3 commands)](#quick-start-3-commands)
- [Concepts](#concepts)
- [Versioning: commits, branches, and merges](#versioning-commits-branches-and-merges)
- [gRPC API](#grpc-api)
- [`priompt` CLI](#priompt-cli)
- [Client libraries](#client-libraries)
- [Validation](#validation)
- [Authentication & TLS](#authentication--tls)
- [Storage](#storage)
- [Caching](#caching)
- [Semantic Propagation Diff](#semantic-propagation-diff)
- [Authoring with `promptctl`](#authoring-with-promptctl)
- [Pub/sub distribution](#pubsub-distribution)
- [Observability & rate limiting](#observability--rate-limiting)
- [Configuration reference](#configuration-reference)
- [Regenerating code from the proto](#regenerating-code-from-the-proto)
- [Project layout](#project-layout)
- [Release history](#release-history)

## Overview

A prompt is a small versioned record:

| Field | Example | Meaning |
| --- | --- | --- |
| `uri` | `priompt://acme/onboarding/welcome` | unique address of the prompt |
| `template` | `Hi {name}, welcome to {org}!` | the text, with `{slot}` placeholders |
| `slots` | `["name", "org"]` | the variables the template expects |
| `version_hash` | `80ec4e4d…` | sha256 of template + slots; changes when content changes |

Agents fetch the template and fill in the slots themselves. Every prompt is
validated both when written and when served, so a malformed prompt can never
reach an agent.

The server provides:

- **Versioning** — each change is a commit in a per-prompt history DAG, with
  branches, merges, and a content hash. See
  [Versioning](#versioning-commits-branches-and-merges).
- **Serving** — agents call `GetPrompt(uri)` over gRPC and receive the template,
  slots, and version hash.
- **Semantic diff** — a propagation diff that measures how far an edit's meaning
  shift ripples through a prompt, distinguishing a local tweak from a structural
  rewrite. See [Semantic Propagation Diff](#semantic-propagation-diff).
- **Caching** — opt-in client- and server-side caches keyed by URI, with an
  optional shared Redis backend.
- **Distribution** — an embedded NATS server notifies subscribers when a prompt
  changes.
- **Operations** — token auth with org scoping and expiry, TLS/mTLS, Prometheus
  metrics, audit logs, per-org rate limiting, backup/restore, and schema
  migrations.

## Installation

Requirements:

- **Go** 1.25+ — <https://go.dev/dl/>
- **buf** — <https://buf.build/docs/installation> — the only proto tool needed;
  it uses remote plugins, so no `protoc` or plugin installs.

Build:

```sh
make build        # buf generate + go build -> ./priompt(.exe)
make test         # go test ./...
```

Or by hand:

```sh
buf generate                            # proto -> Go stubs
go build -o priompt ./cmd/priompt   # build the binary
go test ./...                           # run tests
```

Run with Docker instead:

```sh
docker compose up        # builds and serves on :8443 (gRPC), :2112 (metrics), :4222 (NATS)
```

## Quick start (3 commands)

No clone, no Go toolchain. The server auto-seeds a demo prompt on first start,
so `get` works immediately. (Image/dist names are placeholders — see
[Release history](#release-history) for the real ones.)

```sh
# 1. run the server (seeds priompt://acme/onboarding/welcome on an empty db)
docker run -d -p 8443:8443 -p 4222:4222 <registry>/<image>

# 2. install the Python client (imports as `priompt`)
pip install <dist-name>
```

```python
# 3. your code
from priompt import PromptClient

client = PromptClient(host="localhost:8443")
prompt = client.get("priompt://acme/onboarding/welcome")
print(prompt.template.format(name="Sujal", org="Acme"))   # Hi Sujal, welcome to Acme!
```

Set `PRIOMPT_SEED=false` (or `serve -seed=false`) to disable the demo prompt in
real deployments.

## Quick start (from source)

```sh
# 1. write a template
echo "Hi {name}, welcome to {org}!" > welcome.txt

# 2. store it locally (declares the two slots; validation runs here)
./priompt put -uri priompt://acme/onboarding/welcome \
  -file welcome.txt -slot name -slot org
# -> stored priompt://acme/onboarding/welcome (80ec4e4d88e6)

# 3. serve it (set a token to require auth; omit PRIOMPT_TOKEN for open access)
PRIOMPT_TOKEN=secret ./priompt serve -addr :8443
```

Fetch it from an agent (Python):

```python
from priompt import PromptClient

client = PromptClient(host="localhost:8443", token="secret")
prompt = client.get("priompt://acme/onboarding/welcome")
text = prompt.template.format(name="Sujal", org="Acme")
```

Storing a prompt whose template and slots disagree fails and writes nothing:

```sh
./priompt put -uri priompt://acme/bad/x -file welcome.txt -slot name
# -> validation failed: template uses undeclared slots: org   (exit 1)
```

## Concepts

- **URI** — every prompt has a unique address: `priompt://org/repo/name`. It is
  stored and looked up as a plain string. The first path segment is the **org**,
  used for auth scoping; **everything after it is a free-form path of any depth**,
  so a repo is a filesystem — `priompt://acme/support/agents/tier1/greeting` is
  a valid prompt. Only the leading `org` segment is load-bearing.
- **Repo** — just a URI prefix (`priompt://org/repo/…`); there is no separate
  repo object. Browse one like a directory tree with `ListPrompts` /
  `priompt list` (see [`priompt` CLI](#priompt-cli)). Each prompt is
  versioned independently — the tree is a view, history is per-file.
- **Slot** — a `{name}` placeholder in the template. Declared slots must exactly
  match the placeholders the template uses (see [Validation](#validation)).
- **Version hash** — a sha256 over template and slots. Identical content always
  produces the same hash; it is the cache key and the content identity of a
  version.
- **Commit** — one node in a prompt's history, content-addressed and pointing at
  its parent(s). Carries author, message, and timestamp.
- **Branch / ref** — a named pointer to a commit. `main` is the trunk and the
  served version. Other branches are work in progress until merged.

## Versioning: commits, branches, and merges

Each prompt has its own history DAG. Publishing a change appends a commit; the
`main` branch is the **served HEAD** — what `GetPrompt` returns. Branches let you
develop a change in isolation; merging promotes it to `main`.

```text
main:    A ─────────── M   (M = merge commit, served HEAD)
                ╲      ╱
feature:         B ── C     (published to the branch, invisible to GetPrompt)
```

A commit's identity includes its content **and** its lineage, so reverting to old
content produces a new commit rather than colliding with the original.

Typical flow over the API:

```sh
# publish onto main (default) — moves the served HEAD, notifies subscribers
priompt publish -uri priompt://acme/support/agent -file v1.txt -slot name

# (via a gRPC client) branch, publish onto the branch, then merge back:
#   CreateBranch  uri, name="feature", from="main"
#   PublishPrompt uri, template=…, branch="feature"   # served HEAD unchanged
#   MergeBranch   uri, into="main", from="feature"     # served HEAD now = feature content
#   History       uri, branch="main"                   # the commit log, newest first
#   DiffCommits   uri, from_hash, to_hash              # semantic diff of any two commits
```

### Pinning & rollback

Agents do not have to track HEAD. `GetPrompt(uri, ref)` fetches a specific branch
tip or commit, so an agent can **pin** a version and upgrade deliberately; the
response carries the `commit_hash` to pin to. `SetBranch(uri, branch, commit)`
points a branch at any existing commit — pointing `main` at an older commit is an
instant, atomic **rollback** that immediately changes what every HEAD reader gets
(cache invalidated, subscribers notified).

```sh
# (via a gRPC client)
#   GetPrompt  uri, ref="v3"            # or a commit hash, or a branch name
#   SetBranch  uri, branch="main", commit_hash=<old>   # roll the served version back
```

Behavior notes:

- **Publishing to a non-`main` branch** records a commit but does not change the
  served HEAD, invalidate the cache, or notify subscribers — branch work is
  invisible until merged.
- **Merging** records a two-parent merge commit on the target branch. Merge
  content is taken from the source branch ("take theirs"); the merge then shows
  up as a diff against the first parent. Three-way content auto-merge is not yet
  implemented.
- **History** walks the first-parent chain (the mainline), newest first.

## gRPC API

Defined in
[proto/priompt/v1/prompt.proto](proto/priompt/v1/prompt.proto):

```proto
service PromptService {
  rpc GetPrompt(GetPromptRequest)       returns (GetPromptResponse);
  rpc DiffPrompt(DiffPromptRequest)     returns (DiffPromptResponse);   // diff stored vs. an edit
  rpc PublishPrompt(PublishPromptRequest) returns (PublishPromptResponse);
  rpc History(HistoryRequest)           returns (HistoryResponse);      // commit log of a branch
  rpc CreateBranch(CreateBranchRequest) returns (CreateBranchResponse);
  rpc MergeBranch(MergeBranchRequest)   returns (MergeBranchResponse);
  rpc DiffCommits(DiffCommitsRequest)   returns (DiffPromptResponse);   // diff any two commits
  rpc SetBranch(SetBranchRequest)       returns (SetBranchResponse);    // rollback / pin a branch
  rpc ListPrompts(ListPromptsRequest)   returns (ListPromptsResponse);  // browse a repo (URI prefix) as a tree
}
```

`GetPromptRequest` takes an optional `ref` (a branch name or commit hash) to fetch
a pinned version instead of the served HEAD; the response then includes the
`commit_hash` served. `SetBranch` points a branch at an existing commit — moving
`main` is an instant rollback.

Key messages:

```proto
message PublishPromptRequest {
  string uri = 1;
  string template = 2;
  repeated string slots = 3;
  string message = 4;   // optional commit message
  string branch  = 5;   // target branch; empty = "main"
}

message Commit {
  string hash = 1;
  string version_hash = 2;
  string parent = 3;
  string parent2 = 4;   // set only on merge commits
  string author = 5;
  string message = 6;
  string created_at = 7;  // RFC3339
}

message HistoryRequest      { string uri = 1; string branch = 2; }       // branch empty = "main"
message CreateBranchRequest { string uri = 1; string name = 2; string from = 3; }
message MergeBranchRequest  { string uri = 1; string into = 2; string from = 3; string message = 4; }
message DiffCommitsRequest  { string uri = 1; string from_hash = 2; string to_hash = 3; }
```

Errors are standard gRPC status codes:

| Code | When |
| --- | --- |
| `NotFound` | no prompt, branch, or commit at the given identifier |
| `InvalidArgument` | the prompt failed validation, or a required field is empty |
| `Unauthenticated` | missing, wrong, or expired token |
| `PermissionDenied` | the token's org scope does not match the URI's org |
| `DataLoss` | a stored prompt failed serve-time validation |
| `Internal` | storage or lookup error |

## `priompt` CLI

```text
priompt init       first-run setup: mint an admin token + print a PRIOMPT_URL
priompt serve      run the gRPC server
priompt put        validate and store a prompt in a local database
priompt diff       semantic propagation diff (stored vs. an edited file) via a server
priompt list       browse a repo (URI prefix) like a filesystem, via a server
priompt publish    publish a new version through a server and notify subscribers
priompt watch      subscribe to a prompt's change events (NATS)
priompt backup     dump every prompt as JSON lines
priompt restore    load prompts from a JSON-lines dump
priompt migrate    apply pending schema migrations and print the version
priompt gen-token  print a fresh random bearer token
```

Selected flags:

- **`serve`** — `-addr`, `-db`, `-tls-cert`, `-tls-key`, `-client-ca` (mTLS),
  `-tokens-file`, `-cache-ttl`, `-redis-url`, `-embed-url`, `-embed-model`,
  `-nats-addr`, `-metrics-addr`, `-rate-limit`, `-rate-burst`.
- **`put`** — `-uri`, `-file` (`-` for stdin), `-slot` (repeatable), `-db`,
  `-force`, `-embed-url`, `-embed-model`.
- **`diff` / `publish`** — `-addr`, `-uri`, `-file`, `-slot`, `-tls`, `-ca-cert`,
  `-cert`, `-key`.
- **`list`** — `-addr`, `-prefix` (URI prefix to browse; empty = everything you're
  scoped to), `-tls`, `-ca-cert`, `-cert`, `-key`.

```sh
priompt list -prefix priompt://acme/support/ -addr localhost:8443
# priompt://acme/support/tier1/agent   1e8284f35650
# priompt://acme/support/tier2/agent   80ec4e4d88e6
```

`put` runs a pre-commit semantic check: when it overwrites an existing prompt it
diffs the stored version against your edit, prints the report, and **refuses a
structural change** unless you pass `-force`.

See [Configuration reference](#configuration-reference) for environment variables.

## Client libraries

### Python

```python
from priompt import PromptClient

client = PromptClient(host="localhost:8443", token="secret")
prompt = client.get("priompt://acme/onboarding/welcome")

print(prompt.template)      # Hi {name}, welcome to {org}!
print(list(prompt.slots))   # ['name', 'org']
print(prompt.version_hash)  # 80ec4e4d…

# Browse a repo (URI prefix) like a filesystem:
for e in client.list("priompt://acme/support/"):
    print(e.uri, e.version_hash)
```

`PromptClient(host=None, token=None, tls=False, ca_cert=None, cache_ttl=0, nats_url=None, url=None)`:

- `host` — `address:port` of the server. Optional if `url` or `PRIOMPT_URL` is set.
- `url` — a full `priompt://<token>@host:port` connection string; defaults to
  `PRIOMPT_URL`. `PromptClient()` with no args connects using that env var.
- `token` — sent as `authorization: Bearer <token>`; omit if the server has no
  token configured, or let the `url` carry it.
- `tls` / `ca_cert` — use TLS, optionally pinning a CA certificate.
- `cache_ttl` — client-side (L1) cache TTL in seconds.
- `nats_url` — endpoint for `subscribe()`.

Install with `pip install <dist-name>` (imports as `priompt`). The client lives
in its own repo: **python-sdk**. `subscribe()` needs the `[nats]` extra.

### JavaScript (Node)

The Node adapter mirrors the Python one and lives in its own repo: **js-sdk**.
It loads the `.proto` at runtime, so there is no codegen:

```js
const { PromptClient } = require("priompt-client");
const client = new PromptClient({ host: "localhost:8443", token: "secret" });
const prompt = await client.get("priompt://acme/onboarding/welcome");
console.log(prompt.template, prompt.version_hash);
```

`npm i @grpc/grpc-js @grpc/proto-loader` (and `nats` for `subscribe()`).

> The Python client covers `get`, `list`, `diff`, and `subscribe` (the Node client
> covers `get`, `diff`, `publish`, `subscribe`). The branch, merge,
> and history RPCs are reachable through the generated stubs (`gen/`, or the
> SDK repos) or any gRPC client using the `.proto`.

## Validation

A prompt is valid when
([internal/validate/validate.go](internal/validate/validate.go)):

1. The URI is non-empty.
2. The template is non-empty.
3. No slot name is empty.
4. Every `{placeholder}` in the template is a declared slot (no **undeclared** slots).
5. Every declared slot appears in the template (no **unused** slots).

Placeholders match `{word}` (letters, digits, underscore). The same function
gates both writes (`put`, `publish`) and reads (`GetPrompt`), so the rules cannot
drift between them.

## Authentication & TLS

**Setup.** `priompt init` mints an admin (write) token, writes it to a tokens
file, and prints a ready-to-use `PRIOMPT_URL` — the one-command first-run path.
It refuses to overwrite an existing tokens file unless `-force`.

```sh
priompt init                                   # -> tokens.txt + a PRIOMPT_URL to export
priompt serve -tokens-file tokens.txt
export PRIOMPT_URL=priompt://<token>@localhost:8443
```

**Connection URL.** Clients take one env var, `PRIOMPT_URL` (Strapi/mongodb
style): `priompt://<token>@host:port` carries both address and credential, so
moving between local, self-hosted, and cloud is a one-var change. The Go CLI, the
Python client, and the Node client all read it. Precedence: an explicit
host/`-addr` wins over the URL's host; an explicit `PRIOMPT_TOKEN`/`token=`
wins over the URL's token. A value with no scheme is treated as a bare host.

**Auth.** Set `PRIOMPT_TOKEN` to require a bearer token; every request must then
send `authorization: Bearer <token>` (checked in constant time). With no tokens
configured the server runs open, for local development.

For multiple keys, org scoping, and write grants, pass `-tokens-file tokens.txt`
with `token [org] [expiry] [rw]` lines:

```text
# tokens.txt
s3cr3t-admin                       # admin, read-only: every org, never expires
admin-rw                    rw     # admin, may write
acme-read     acme                 # scoped read-only: only priompt://acme/…
acme-author   acme         rw      # scoped, may write
rotating-key  acme  2026-12-31 rw  # scoped + expires (date or RFC3339) + write
```

A bare token is **admin** (all orgs); a token with an org is scoped to
`priompt://org/…` and gets `PermissionDenied` for other orgs. **Write is
opt-in:** a token may only publish, branch, merge, or roll back if its line
carries the `rw` keyword — every token is read-only otherwise (`ro` is the
explicit default). Read-only tokens get `PermissionDenied` on mutating RPCs.
Fields after the token are matched by keyword, so `org`, `expiry`, and `rw` may
appear in any order. `PRIOMPT_TOKEN` is always an admin write key. A past
expiry is rejected with `Unauthenticated`. Rotate by overlapping tokens: issue
the new key, give the old one a near-future expiry, drop it once it lapses.
`priompt gen-token` mints a random token.

> Authorization is org-prefix scoping plus a read/write grant; per-prompt rules
> are not modeled.

**TLS.** Pass `-tls-cert` and `-tls-key` to terminate TLS; on the client set
`tls=True` (and optionally `ca_cert`). Without these the server listens in
plaintext.

**mTLS.** Add `-client-ca ca.pem` to also verify the client's certificate
(`RequireAndVerifyClientCert`); connections without a cert signed by that CA are
refused at the TLS layer, before auth. Requires `-tls-cert`/`-tls-key`.

```sh
priompt serve -tls-cert server.pem -tls-key server.key -client-ca ca.pem
priompt diff  -uri priompt://acme/x -file e.txt -tls \
  -ca-cert ca.pem -cert client.pem -key client.key
```

## Storage

[internal/store/store.go](internal/store/store.go) runs on **SQLite** (pure-Go
`modernc.org/sqlite`, no cgo, single binary — the self-hosted default) or
**PostgreSQL** for multi-node deployments. The backend is chosen from `-db`: a
file path uses SQLite; a `postgres://…` DSN uses Postgres (via `pgx`). The schema
and queries are portable across both; only the driver and placeholder style
differ.

```sh
priompt serve -db priompt.db                            # sqlite (default)
priompt serve -db postgres://user:pass@host:5432/prompts  # postgres
```

Schema:

```sql
-- prompts: the materialized tip of each prompt's "main" branch (the served HEAD)
CREATE TABLE prompts (
  uri          TEXT PRIMARY KEY,
  template     TEXT NOT NULL,
  slots        TEXT NOT NULL,    -- JSON array
  version_hash TEXT NOT NULL
);

-- commits: the history DAG; parent2 is set only on merge commits
CREATE TABLE commits (
  hash         TEXT PRIMARY KEY,
  uri          TEXT NOT NULL,
  template     TEXT NOT NULL,
  slots        TEXT NOT NULL,
  version_hash TEXT NOT NULL,
  parent       TEXT,
  parent2      TEXT,
  author       TEXT NOT NULL,
  message      TEXT NOT NULL,
  created_at   TEXT NOT NULL
);

-- refs: a branch name -> its tip commit
CREATE TABLE refs (
  uri         TEXT NOT NULL,
  branch      TEXT NOT NULL,
  commit_hash TEXT NOT NULL,
  PRIMARY KEY (uri, branch)
);
```

A commit on `main` updates `commits`, `refs`, and `prompts` in one transaction,
so the served HEAD always reflects the latest committed content. `version_hash`
is `sha256(template + "\0" + slots)`, recomputed on every write.

### Encryption at rest

Set `PRIOMPT_ENCRYPTION_KEY` (base64 of a 32-byte key) and the `template` and
`slots` columns are encrypted with **AES-256-GCM** before they touch the disk, in
both the `prompts` and `commits` tables. Unset, storage is plaintext (unchanged).

```sh
export PRIOMPT_ENCRYPTION_KEY=$(head -c32 /dev/urandom | base64)
priompt serve -db priompt.db
```

- **At rest, not end-to-end.** The server decrypts to run validation and the
  semantic diff, so it holds the key; the guarantee is that a stolen database file
  is useless without it. True E2E would move those server-side features to the
  client.
- **`version_hash` stays plaintext** (it is a hash, not the content), so dedup,
  idempotency, and the cache key are unaffected.
- **No migration to enable.** Encrypted values carry a marker, so rows written
  before you set a key stay readable; new writes are encrypted. (Rotation and
  bulk re-encryption of old rows are not automated — re-`put` to re-encrypt.)
- Every process that touches the same database — `serve`, `put`, `backup` — must
  share the key. Lose the key and the encrypted rows are unrecoverable.

### Migrations

The schema is an ordered list of migration steps (`migrations` in
[internal/store/store.go](internal/store/store.go)), each applied once. Every
`Open` runs the pending steps in a transaction and records progress in
`schema_version`, so startup is idempotent and a failed step rolls back cleanly.
To evolve the schema, **append** a step — never edit or reorder an existing one.
`priompt migrate -db …` applies pending steps and prints the version; run it as
a deploy step against Postgres.

### Backup & restore

`backup` dumps every prompt as JSON lines; `restore` upserts them back. The
format is portable, so it doubles as a SQLite↔Postgres migration path, and the
upsert makes restore safe to re-run.

```sh
priompt backup  -db priompt.db -out snapshot.jsonl
priompt restore -db postgres://user:pass@host/prompts -in snapshot.jsonl
```

> Backup/restore covers the served HEAD (the `prompts` table), not the full
> commit history.

## Caching

Two opt-in layers, both keyed by URI with TTL-bounded freshness. Only
**validated** prompts are cached, so a malformed prompt is never served from
cache.

- **Server-side (L2)** — an in-process cache in front of the store. On by
  default; tune with `serve -cache-ttl 30s` (`0` disables).
- **Client-side (L1)** — an in-process cache in the client. Off by default;
  enable with `PromptClient(..., cache_ttl=30)`.

```python
client = PromptClient(host="localhost:8443", token="secret", cache_ttl=30)
client.get("priompt://acme/onboarding/welcome")  # first call hits the server
client.get("priompt://acme/onboarding/welcome")  # served from L1 for 30s
```

For multi-node deployments, point L2 at **Redis** so instances share one cache:

```sh
priompt serve -redis-url redis://localhost:6379/0   # or PRIOMPT_REDIS_URL
```

Both backends implement the same `Cache` interface
([internal/server/cache.go](internal/server/cache.go),
[internal/server/redis_cache.go](internal/server/redis_cache.go)); responses are
stored as marshaled protobuf with the TTL as key expiry. Every cache op is
best-effort — a Redis hiccup degrades to a cache miss, never a serving error.

## Semantic Propagation Diff

A text diff tells you *that* a line changed. This tells you *how far the meaning
shift ripples* through the surrounding prompt — the difference between a safe
local tweak and a structural change that quietly rewires how nearby instructions
relate.

For each changed hunk it measures three signals
([internal/semdiff/semdiff.go](internal/semdiff/semdiff.go)):

1. **Signal 1** — the changed region (a line-level LCS diff hunk).
2. **Signal 2** — semantic delta at the point of change: `1 - cosine(old, new)`.
3. **Signal 3** — propagation profile: grow the window outward (±2, ±4, ±6 …), up
   and down independently, recomputing the delta until the curve flattens
   (propagation stopped) or hits the file boundary.

High Signal 2 + flat Signal 3 → **localized tweak**. Signal 3 still high at the
boundary → **structural** (the dangerous one).

The diff runs **server-side**, against the **stored** prompt, using the embedding
model the **operator configured at startup** — so analysis is consistent for
everyone and prompts never leave your infrastructure. With no URL configured the
server falls back to an offline, zero-dependency *lexical* embedder (hashed
bag-of-words) that scores overlap, not meaning. Point `-embed-url` at any
OpenAI-compatible endpoint (Ollama, text-embeddings-inference, llama.cpp) for
real semantics:

```sh
priompt serve -embed-url http://localhost:11434/v1/embeddings -embed-model nomic-embed-text
# (also reads PRIOMPT_EMBED_URL / _MODEL / _KEY)
```

```sh
priompt diff -uri priompt://acme/support/agent -file edited.txt -addr localhost:8443
# change @ new lines 3-3 (old 3-3): replace
#   Signal 2 (point delta): 0.470
#   Signal 3 up:   ±2=0.095 (boundary)
#   Signal 3 down: ±2=0.153 ±4=0.153 (flat)
#   => localized tweak
```

`DiffPrompt` diffs a stored prompt against an edit; `DiffCommits` diffs any two
commits in history. The same engine powers `promptctl diff` for local authoring.

## Authoring with `promptctl`

`promptctl`, the git-backed authoring CLI, lives in its own repo: **cli**. It
keeps prompts as `*.prompt` files in a git repo (via go-git) and adds
validation, the semantic propagation diff, and environment promotion on top
(commit / diff / log / promote / push / pull).

## Pub/sub distribution

The server embeds a **NATS** server in-process (still one binary). The
write-through `PublishPrompt` RPC stores a new version *and* notifies subscribers.

```sh
# server: embedded NATS is on by default (-nats-addr "" disables it)
priompt serve -nats-addr 127.0.0.1:4222

# publish a new version -> stored, cache invalidated, subscribers notified
priompt publish -uri priompt://acme/support/agent -file v2.txt -slot name
# published priompt://acme/support/agent (1e8284f35650) — subscribers notified
```

Each prompt maps to a NATS subject — `priompt://acme/support/agent` →
`priompt.acme.support.agent`. An agent subscribes in one line (the push side);
`cache_ttl` is the pull side.

**Notifications carry the diff verdict.** Each event includes the
[Semantic Propagation Diff](#semantic-propagation-diff) classification of the
change (`structural | localized tweak | minor edit | new`), so an agent can
auto-hot-reload a localized tweak but **hold a structural change for review** —
the server already computed the diff on publish.

```python
client = PromptClient(host="…:8443", cache_ttl=30, nats_url="nats://…:4222")

def on_change(version, classification):
    if classification == "structural":
        alert_a_human(version)          # reshapes meaning — gate it
    else:
        reload(version)                 # safe to pick up automatically

client.subscribe("priompt://acme/support/agent", on_change)  # needs `pip install nats-py`
```

The `priompt watch` CLI prints the verdict too, and exposes it to `-exec` hooks
as `PRIOMPT_CLASS`.

**Consistency model.** This layer is eventually consistent: a push can be missed
(network), so the TTL is the convergence guarantee, not the push. Notify is
best-effort; the version is durably stored regardless, and subscribers converge
on their next poll.

## Observability & rate limiting

Three gRPC interceptors run in front of every RPC
([internal/server/observability.go](internal/server/observability.go)):

- **Metrics** — a Prometheus endpoint at `-metrics-addr` (default `:2112`, empty
  disables) exposing request counts (`priompt_requests_total` by method + code),
  a latency histogram (`priompt_request_duration_seconds`), and Go runtime
  metrics. A sample scrape config is in
  [monitoring/prometheus.yml](monitoring/prometheus.yml).
- **Audit log** — one structured (slog JSON) line per RPC on stderr: method, org
  scope, uri, gRPC code, latency.
- **Rate limiting** — a per-org token bucket. Opt-in via `-rate-limit <rps>`
  (`0` disables); `-rate-burst` sets the burst (defaults to the rps). Over-limit
  calls get `ResourceExhausted`.

```sh
priompt serve -metrics-addr :2112 -rate-limit 50 -rate-burst 100
curl localhost:2112/metrics
```

## Configuration reference

Environment variables:

| Variable | Used by | Purpose |
| --- | --- | --- |
| `PRIOMPT_URL` | clients | `priompt://<token>@host:port` — one string for address + credential |
| `PRIOMPT_TOKEN` | server, clients | admin bearer token (server); credential (clients) |
| `PRIOMPT_EMBED_URL` | server, `put` | OpenAI-compatible embeddings endpoint |
| `PRIOMPT_EMBED_MODEL` | server, `put` | embedding model name |
| `PRIOMPT_EMBED_KEY` | server, `put` | API key for the embeddings endpoint |
| `PRIOMPT_REDIS_URL` | server | Redis URL for a shared L2 cache |
| `PRIOMPT_ENCRYPTION_KEY` | server, `put`, `backup` | base64 32-byte key; encrypts `template`/`slots` at rest (AES-256-GCM) |
| `PRIOMPT_GIT_TOKEN` | `promptctl` | token for HTTPS git remotes |

CLI flags are listed under [`priompt` CLI](#priompt-cli); the
[docker-compose.yml](docker-compose.yml) shows a full `serve` invocation.

## Regenerating code from the proto

Everything under `gen/` is generated. After editing the `.proto`:

```sh
buf generate
```

[buf.gen.yaml](buf.gen.yaml) configures the output (Go into `gen/`) using buf's
remote plugins, so no `protoc` or plugin installs are needed. Do not hand-edit
generated files. The SDK repos (**python-sdk**, **js-sdk**) carry their own copy
of the proto; keep them in sync when the contract changes.

## Project layout

```text
proto/priompt/v1/prompt.proto    The service contract. Source of truth.
gen/                               Generated Go code (do not edit).

internal/validate/validate.go      Prompt well-formedness — the one definition of "valid".
internal/store/store.go            SQLite/Postgres storage: prompts, commits, refs, migrations.
internal/semdiff/semdiff.go        The semantic propagation diff engine.
internal/server/server.go          gRPC handlers + auth interceptor.
internal/server/observability.go   Metrics, audit-log, rate-limit interceptors.
internal/server/cache.go           In-process and Redis L2 caches.
internal/pubsub/pubsub.go          Embedded NATS publisher + subscriber.

cmd/priompt/main.go              Server + operational CLI.
```

**Serve request flow:** agent → gRPC → auth interceptor → `Server.GetPrompt` →
`store.Get` → serve-time `validate.Prompt` → response.

## Release history

| Version | Ships |
| --- | --- |
| **v0.1** | gRPC server, Python adapter, validation |
| **v0.2** | L1/L2 caching (TTL), keyed on `version_hash` |
| **v0.3** | `promptctl` CLI: commit/diff/log/promote/push/pull, go-git versioning |
| **v0.4** | Pub/sub distribution over embedded NATS, TTL sync, subscriber model |
| **v0.5** | PostgreSQL backend, Redis L2 cache, org-scoped multi-token auth |
| **v0.6** | Token expiry/rotation, mTLS, Prometheus metrics + audit log, per-org rate limiting, backup/restore, schema migrations |
| **v0.7** | Server-side versioning: commit DAG, branches, merges, history, commit-to-commit diff, version pinning (`GetPrompt` by ref) + rollback (`SetBranch`), and diff-verdict change notifications |
