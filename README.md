# Priompt

**A home for your AI prompts — with version history, instant rollback, and live
updates to every agent using them.**

If you have ever changed a prompt, broken an AI agent in production, and had no
idea what the old prompt said — Priompt is for you.

## The 30-second version

Teams already solved this problem once, for code. Nobody emails `final_v2.js`
around anymore; code lives in Git, every change is recorded, and you can always
roll back. Priompt does the same job for **prompts**:

| Code has… | Prompts get… |
| --- | --- |
| A Git server | A Priompt server (this repo) |
| A file path | A prompt address: `priompt://acme/onboarding/welcome` |
| Commits & branches | Commits & branches, per prompt |
| `git diff` | A **semantic diff** — "did the *meaning* change, and how far did it spread?" |
| Deploys | Live push notifications to every agent the moment a prompt changes |

You run the server. Your prompts live in **your** infrastructure. Your agents —
written in any language — fetch prompts by address instead of hard-coding them.

```mermaid
flowchart LR
    subgraph Authors["✍️ Prompt authors"]
        CTL["promptctl CLI<br/>(the cli repo)"]
        OPS["priompt publish"]
    end
    subgraph Server["🗄️ Priompt server (this repo)"]
        VAL["Validation"]
        STORE["Versioned store<br/>(SQLite / Postgres)"]
        DIFF["Semantic diff engine"]
        NATS["Change notifier<br/>(embedded NATS)"]
    end
    subgraph Agents["🤖 Your AI agents"]
        PY["Python app<br/>(python-sdk)"]
        JS["Node app<br/>(js-sdk)"]
        ANY["Any gRPC client"]
    end
    Authors -->|"publish a new version"| Server
    Server -->|"serve by URI"| Agents
    NATS -.->|"'prompt changed!' push"| Agents
```

Two capabilities are the reason Priompt exists — everything else supports them:

1. **The Semantic Propagation Diff** — a diff that measures *how far an edit's
   meaning-shift ripples* through a prompt, separating a safe local tweak from a
   structural rewrite. A text diff cannot tell you this.
   → [Semantic Propagation Diff](#the-semantic-propagation-diff)
2. **Change distribution** — prompts are live, versioned resources. The server
   pushes a notification to every subscribed agent the instant one changes,
   *including the diff verdict*, so agents can auto-reload safe changes and hold
   risky ones for review. → [When a prompt changes](#when-a-prompt-changes-pubsub)

## The Priompt family

This is the **core server** repo. The ecosystem is six small repos:

| Repo | What it is | Who uses it |
| --- | --- | --- |
| **priompt** (this one) | The server: stores, versions, validates, serves, and distributes prompts | Whoever runs the infrastructure |
| **proto** | The shared source of truth: the gRPC contract, JWT claims, validation rules, semantic diff engine | Every other repo (imports, not copies) |
| **cli** | `promptctl` — authoring tool; prompts as files in git, with validation and semantic diff | Prompt writers |
| **auth** | `priompt-auth` — token issuer: SSO logins and service accounts become short-lived JWTs | Enterprises needing SSO, rotation, offboarding |
| **python-sdk** | Python client library | Python agents/apps |
| **js-sdk** | JavaScript client library | Node agents/apps |
| **db-adapters** | The storage engine as a reusable library (SQLite, PostgreSQL) | Tool builders |

## Contents

- [Quick start (Docker, 3 commands)](#quick-start-docker-3-commands)
- [Quick start (from source)](#quick-start-from-source)
- [What exactly is a "prompt" here?](#what-exactly-is-a-prompt-here)
- [How an agent gets a prompt](#how-an-agent-gets-a-prompt)
- [Version history: commits, branches, merges](#version-history-commits-branches-merges)
- [When a prompt changes (pub/sub)](#when-a-prompt-changes-pubsub)
- [The Semantic Propagation Diff](#the-semantic-propagation-diff)
- [The `priompt` command](#the-priompt-command)
- [Client libraries](#client-libraries)
- [Security: tokens, orgs, TLS](#security-tokens-orgs-tls)
- [Storage](#storage)
- [Caching](#caching)
- [Monitoring & rate limiting](#monitoring--rate-limiting)
- [Configuration reference](#configuration-reference)
- [gRPC API reference](#grpc-api-reference)
- [For developers of Priompt itself](#for-developers-of-priompt-itself)
- [Release history](#release-history)

## Quick start (Docker, 3 commands)

No clone, no toolchain. The server seeds a demo prompt on first start, so
fetching works immediately. (Image/dist names are placeholders until the first
release.)

```sh
# 1. run the server
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
real deployments. `docker compose up` in this repo does the same thing with
persistent storage — see [docker-compose.yml](docker-compose.yml).

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

## What exactly is a "prompt" here?

A prompt is a small versioned record — four fields:

| Field | Example | In plain words |
| --- | --- | --- |
| `uri` | `priompt://acme/onboarding/welcome` | Its unique address |
| `template` | `Hi {name}, welcome to {org}!` | The text, with `{placeholders}` |
| `slots` | `["name", "org"]` | The blanks the template expects to be filled |
| `version_hash` | `80ec4e4d…` | A fingerprint of the content — same text, same fingerprint |

Agents fetch the template and fill the slots themselves.

**Addresses.** The first path segment of a URI is the **org** (used for access
control); everything after it is a free-form path of any depth —
`priompt://acme/support/agents/tier1/greeting` is valid. A "repo" is just a URI
prefix you can browse like a folder (`priompt list`); there is no separate repo
object, and each prompt has its own independent history.

**Validation.** A prompt is valid when (`priomptproto/validate`, in the **proto** repo):

1. The URI is non-empty.
2. The template is non-empty.
3. No slot name is empty.
4. Every `{placeholder}` in the template is a declared slot (nothing undeclared).
5. Every declared slot appears in the template (nothing unused).

Placeholders match `{word}` (letters, digits, underscore). The **same function**
runs when a prompt is written *and* when it is served, so a malformed prompt can
never reach an agent — not even from a corrupted database.

## How an agent gets a prompt

```mermaid
sequenceDiagram
    participant A as 🤖 Agent
    participant C as Client library (L1 cache)
    participant S as Priompt server
    participant DB as Store

    A->>C: get("priompt://acme/onboarding/welcome")
    alt fresh in L1 cache
        C-->>A: cached prompt (no network)
    else
        C->>S: GetPrompt over gRPC (+ Bearer token)
        S->>S: auth check (token, org scope)
        alt fresh in L2 cache
            S-->>C: cached response
        else
            S->>DB: read prompt
            S->>S: validate before serving
            S-->>C: template + slots + version_hash
        end
        C-->>A: prompt
    end
```

Every read passes the auth interceptor and (on a cache miss) serve-time
validation. Full flow in code: agent → gRPC → auth interceptor →
`Server.GetPrompt` → `store.Get` → `validate.Prompt` → response.

## Version history: commits, branches, merges

Every prompt has its own history, exactly like a file in Git: each published
change is a **commit** (with author, message, timestamp), a **branch** is a named
line of work, and **`main` is what agents are served** (the "served HEAD").

```mermaid
gitGraph
    commit id: "A: first version"
    branch feature
    commit id: "B: experiment"
    commit id: "C: refined"
    checkout main
    merge feature id: "M: merged - now served"
```

Work published to a `feature` branch is recorded but **invisible to agents**
until merged into `main`. A commit's identity covers its content *and* its
lineage, so reverting to old content creates a new commit rather than colliding
with the original.

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

Agents do not have to follow the latest version. `GetPrompt(uri, ref)` fetches a
specific branch tip or commit, so an agent can **pin** a version and upgrade
deliberately — the response carries the `commit_hash` to pin to.

`SetBranch(uri, "main", <old-commit>)` points `main` at any earlier commit: an
instant, atomic **rollback**. Every HEAD reader gets the old version immediately
(cache invalidated, subscribers notified). Nothing is deleted — a rollback is
just moving a pointer.

Behavior notes:

- **Publishing to a non-`main` branch** records a commit but does not change the
  served HEAD, invalidate the cache, or notify subscribers.
- **Merging** records a two-parent merge commit. Merge content is taken from the
  source branch ("take theirs"); three-way content auto-merge is not yet
  implemented.
- **History** walks the first-parent chain (the mainline), newest first.

## When a prompt changes (pub/sub)

Prompts are not static files an agent reads once. The server embeds a **NATS**
message broker in-process (still a single binary), and `PublishPrompt` stores
the new version *and* pushes a notification to every subscriber:

```mermaid
sequenceDiagram
    participant W as ✍️ Author
    participant S as Priompt server
    participant N as Embedded NATS
    participant A as 🤖 Subscribed agent

    W->>S: publish new version
    S->>S: validate + commit + run semantic diff
    S->>S: invalidate cache
    S->>N: notify subject priompt.acme.support.agent
    N-->>A: "changed! version 1e8284…, verdict: localized tweak"
    alt verdict is safe (tweak / minor edit)
        A->>A: hot-reload the new version
    else verdict is "structural"
        A->>A: keep the old version, alert a human
    end
```

Each prompt address maps to a NATS subject: `priompt://acme/support/agent` →
`priompt.acme.support.agent`.

**Notifications carry the diff verdict.** Each event includes the
[Semantic Propagation Diff](#the-semantic-propagation-diff) classification
(`structural | localized tweak | minor edit | new`) — the server already
computed it on publish. That is what makes safe auto-reload possible:

```python
client = PromptClient(host="…:8443", cache_ttl=30, nats_url="nats://…:4222")

def on_change(version, classification):
    if classification == "structural":
        alert_a_human(version)          # reshapes meaning — gate it
    else:
        reload(version)                 # safe to pick up automatically

client.subscribe("priompt://acme/support/agent", on_change)  # needs `pip install nats-py`
```

The `priompt watch` command prints the verdict too, and exposes it to `-exec`
hooks as `PRIOMPT_CLASS`.

**Consistency model.** Push is best-effort (a network blip can drop one); the
client-side cache TTL is the convergence guarantee. The new version is durably
stored regardless, and subscribers converge on their next fetch.

```sh
# server: embedded NATS is on by default (-nats-addr "" disables it)
priompt serve -nats-addr 127.0.0.1:4222

# publish a new version -> stored, cache invalidated, subscribers notified
priompt publish -uri priompt://acme/support/agent -file v2.txt -slot name
# published priompt://acme/support/agent (1e8284f35650) — subscribers notified
```

## The Semantic Propagation Diff

A text diff tells you *that* line 3 changed. It cannot tell you whether that
change quietly rewired how the rest of the prompt behaves. Priompt's diff can.

The intuition: drop a stone in a pond and watch how far the ripples travel.

```text
Edit on line 3:  "Be helpful"  ->  "Never help with refunds"

           line 1  ────────────────  meaning unchanged?
           line 2  ──────────────    meaning unchanged?      ← ripples measured
  [EDIT]   line 3  ██████████████    the change itself         outward, ±2, ±4,
           line 4  ──────────────    meaning unchanged?         ±6 lines… until
           line 5  ────────────────  meaning unchanged?         they flatten out
```

For each changed hunk, three signals (`priomptproto/semdiff`, in the **proto** repo):

1. **Signal 1 — where.** The changed region (a line-level LCS diff hunk).
2. **Signal 2 — how big at the point.** Semantic distance between old and new
   text right at the change: `1 - cosine(old, new)` using embeddings.
3. **Signal 3 — how far it spreads.** Re-measure with a growing window (±2, ±4,
   ±6 lines…), up and down independently, until the curve flattens (the ripple
   stopped) or hits the edge of the prompt.

The verdict:

| Signal 2 (at the point) | Signal 3 (the ripple) | Verdict |
| --- | --- | --- |
| high | flattens quickly | **localized tweak** — safe |
| low | flat | **minor edit** — safe |
| any | still high at the boundary | **structural** — the dangerous one, review it |

The diff runs **server-side**, against the **stored** prompt, with the embedding
model the operator configured at startup — analysis is consistent for everyone
and prompts never leave your infrastructure. With no model configured the server
falls back to an offline, zero-dependency *lexical* embedder (hashed
bag-of-words) that scores word overlap, not meaning. Point `-embed-url` at any
OpenAI-compatible endpoint (Ollama, text-embeddings-inference, llama.cpp) for
real semantics:

```sh
priompt serve -embed-url http://localhost:11434/v1/embeddings -embed-model nomic-embed-text
# (also reads PRIOMPT_EMBED_URL / _MODEL / _KEY)
```

Sample output:

```sh
priompt diff -uri priompt://acme/support/agent -file edited.txt -addr localhost:8443
# change @ new lines 3-3 (old 3-3): replace
#   Signal 2 (point delta): 0.470
#   Signal 3 up:   ±2=0.095 (boundary)
#   Signal 3 down: ±2=0.153 ±4=0.153 (flat)
#   => localized tweak
```

`DiffPrompt` diffs a stored prompt against an edit; `DiffCommits` diffs any two
commits in history. The same engine powers `promptctl diff` (the **cli** repo)
for local authoring.

## The `priompt` command

One binary, two jobs: run the server, and operate it.

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
  `-nats-addr`, `-metrics-addr`, `-rate-limit`, `-rate-burst`,
  `-auth-jwks-url` (trust priompt-auth JWTs).
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

## Client libraries

Full docs live in each SDK repo; the short version:

**Python** (repo: **python-sdk**, imports as `priompt`) — covers `get`, `list`,
`diff`, `subscribe`:

```python
from priompt import PromptClient

client = PromptClient(host="localhost:8443", token="secret")
prompt = client.get("priompt://acme/onboarding/welcome")

print(prompt.template)      # Hi {name}, welcome to {org}!
print(list(prompt.slots))   # ['name', 'org']
print(prompt.version_hash)  # 80ec4e4d…

for e in client.list("priompt://acme/support/"):   # browse a repo like a folder
    print(e.uri, e.version_hash)
```

**JavaScript** (repo: **js-sdk**, package `priompt-client`) — covers `get`,
`diff`, `publish`, `subscribe`:

```js
const { PromptClient } = require("priompt-client");
const client = new PromptClient({ host: "localhost:8443", token: "secret" });
const prompt = await client.get("priompt://acme/onboarding/welcome");
console.log(prompt.template, prompt.version_hash);
```

The branch, merge, and history RPCs are reachable through the generated stubs
or any gRPC client using the `.proto` — Priompt is language-agnostic by design.

## Security: tokens, orgs, TLS

```mermaid
flowchart LR
    R["Incoming request<br/>+ Bearer token"] --> T{"Static token known, or<br/>valid priompt-auth JWT?"}
    T -- no --> U["❌ Unauthenticated"]
    T -- yes --> O{"Token's org matches<br/>the URI's org?"}
    O -- no --> P["❌ PermissionDenied"]
    O -- yes --> W{"Write RPC?"}
    W -- "yes, token is read-only" --> P
    W -- "no, or token has rw" --> OK["✅ handled"]
```

**Setup.** `priompt init` mints an admin (write) token, writes it to a tokens
file, and prints a ready-to-use `PRIOMPT_URL` — the one-command first-run path.
It refuses to overwrite an existing tokens file unless `-force`.

```sh
priompt init                                   # -> tokens.txt + a PRIOMPT_URL to export
priompt serve -tokens-file tokens.txt
export PRIOMPT_URL=priompt://<token>@localhost:8443
```

**Connection URL.** Clients take one env var, `PRIOMPT_URL`:
`priompt://<token>@host:port` carries both address and credential, so moving
between local, self-hosted, and cloud is a one-var change. The Go CLI, the
Python client, and the Node client all read it. Precedence: an explicit
host/`-addr` wins over the URL's host; an explicit `PRIOMPT_TOKEN`/`token=` wins
over the URL's token. A value with no scheme is treated as a bare host.

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

- A bare token is **admin** (all orgs); a token with an org only reaches
  `priompt://org/…`.
- **Write is opt-in:** only lines with `rw` may publish, branch, merge, or roll
  back; every token is read-only otherwise.
- Fields after the token are matched by keyword, so `org`, `expiry`, and `rw`
  may appear in any order. A past expiry is rejected with `Unauthenticated`.
- Rotate by overlapping: issue the new key, give the old one a near-future
  expiry, drop it once it lapses. `priompt gen-token` mints a random token.
- `PRIOMPT_TOKEN` is always an admin write key.

> Authorization is org-prefix scoping plus a read/write grant; per-prompt rules
> are not modeled.

**Enterprise tokens (SSO, short-lived).** For SSO logins, service accounts,
and minutes-long credentials, run **priompt-auth** (the auth repo) and point
the server at its key endpoint:

```sh
priompt serve -tokens-file tokens.txt -auth-jwks-url http://auth-host:8444/jwks
```

The server then also accepts the EdDSA JWTs that service issues, verifying
them **offline** against cached `/jwks` keys — no per-request call to the
issuer, and static tokens keep working alongside. A JWT carries the same
permission model (`org` scope, `rw` grant, expiry); SSO group mapping,
rotation, and offboarding live in the issuer and your IdP.

**TLS.** Pass `-tls-cert` and `-tls-key` to terminate TLS; on the client set
`tls=True` (and optionally `ca_cert`). Without these the server listens in
plaintext.

**mTLS.** Add `-client-ca ca.pem` to also verify the client's certificate;
connections without a cert signed by that CA are refused at the TLS layer,
before auth. Requires `-tls-cert`/`-tls-key`.

```sh
priompt serve -tls-cert server.pem -tls-key server.key -client-ca ca.pem
priompt diff  -uri priompt://acme/x -file e.txt -tls \
  -ca-cert ca.pem -cert client.pem -key client.key
```

**Encryption at rest.** See [Storage](#storage) — one env var encrypts prompt
content on disk with AES-256-GCM.

## Storage

Prompts live in **SQLite** (pure-Go driver, no external dependencies — the
self-hosted default) or **PostgreSQL** (multi-node). The backend is chosen from
`-db`: a file path means SQLite; a `postgres://…` DSN means Postgres. The
storage engine is also published as a standalone library — the **db-adapters**
repo — structured so new databases can be added.

```sh
priompt serve -db priompt.db                              # sqlite (default)
priompt serve -db postgres://user:pass@host:5432/prompts  # postgres
```

Three tables:

```mermaid
flowchart TD
    P["prompts<br/>the served HEAD of each prompt<br/>(what agents get right now)"]
    C["commits<br/>the full history DAG<br/>(every version ever published)"]
    R["refs<br/>branch name → tip commit<br/>(main, feature, …)"]
    R -->|"points into"| C
    C -->|"tip of main is materialized into"| P
```

```sql
CREATE TABLE prompts (
  uri          TEXT PRIMARY KEY,
  template     TEXT NOT NULL,
  slots        TEXT NOT NULL,    -- JSON array
  version_hash TEXT NOT NULL
);

CREATE TABLE commits (
  hash TEXT PRIMARY KEY, uri TEXT NOT NULL,
  template TEXT NOT NULL, slots TEXT NOT NULL, version_hash TEXT NOT NULL,
  parent TEXT, parent2 TEXT,     -- parent2 only on merge commits
  author TEXT NOT NULL, message TEXT NOT NULL, created_at TEXT NOT NULL
);

CREATE TABLE refs (
  uri TEXT NOT NULL, branch TEXT NOT NULL, commit_hash TEXT NOT NULL,
  PRIMARY KEY (uri, branch)
);
```

A commit on `main` updates `commits`, `refs`, and `prompts` in one transaction,
so the served HEAD always reflects the latest committed content. `version_hash`
is `sha256(template + "\0" + slots)`, recomputed on every write.

### Encryption at rest

Set `PRIOMPT_ENCRYPTION_KEY` (base64 of a 32-byte key) and the `template` and
`slots` columns are encrypted with **AES-256-GCM** before they touch the disk —
a stolen database file is useless without the key.

```sh
export PRIOMPT_ENCRYPTION_KEY=$(head -c32 /dev/urandom | base64)
priompt serve -db priompt.db
```

- **At rest, not end-to-end** — the server decrypts to run validation and the
  semantic diff, so it holds the key.
- **`version_hash` stays plaintext** (it is a fingerprint, not content), so
  dedup, idempotency, and cache keys are unaffected.
- **No migration to enable** — encrypted values carry a marker; rows written
  before the key stay readable, new writes are encrypted. (Rotation and bulk
  re-encryption are not automated — re-`put` to re-encrypt.)
- Every process touching the same database — `serve`, `put`, `backup` — must
  share the key. **Lose the key and encrypted rows are unrecoverable.**

### Migrations

The schema is an ordered list of migration steps, each applied exactly once.
Every startup runs the pending steps in a transaction and records progress in
`schema_version` — idempotent, and a failed step rolls back cleanly. To evolve
the schema, **append** a step; never edit or reorder an existing one.
`priompt migrate -db …` applies pending steps as a deploy step.

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

```mermaid
flowchart LR
    A["🤖 Agent"] --> L1["L1: in the client<br/>(off by default, cache_ttl=…)"]
    L1 -->|miss| S["Server"]
    S --> L2["L2: in the server<br/>(on by default, -cache-ttl)"]
    L2 -->|miss| DB["Store"]
    L2 -.->|"multi-node option"| RD["Shared Redis<br/>(-redis-url)"]
```

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
[internal/server/redis_cache.go](internal/server/redis_cache.go)). Every cache
op is best-effort — a Redis hiccup degrades to a cache miss, never a serving
error.

## Monitoring & rate limiting

Three gRPC interceptors run in front of every RPC
([internal/server/observability.go](internal/server/observability.go)):

- **Metrics** — a Prometheus endpoint at `-metrics-addr` (default `:2112`,
  empty disables): request counts (`priompt_requests_total` by method + code),
  a latency histogram (`priompt_request_duration_seconds`), and Go runtime
  metrics. Sample scrape config: [monitoring/prometheus.yml](monitoring/prometheus.yml).
- **Audit log** — one structured (slog JSON) line per RPC on stderr: method,
  org scope, uri, gRPC code, latency.
- **Rate limiting** — a per-org token bucket. Opt-in via `-rate-limit <rps>`
  (`0` disables); `-rate-burst` sets the burst (defaults to the rps).
  Over-limit calls get `ResourceExhausted`.

```sh
priompt serve -metrics-addr :2112 -rate-limit 50 -rate-burst 100
curl localhost:2112/metrics
```

## Configuration reference

| Variable | Used by | Purpose |
| --- | --- | --- |
| `PRIOMPT_URL` | clients | `priompt://<token>@host:port` — one string for address + credential |
| `PRIOMPT_TOKEN` | server, clients | admin bearer token (server); credential (clients) |
| `PRIOMPT_SEED` | server | `false` disables the demo prompt on first start |
| `PRIOMPT_EMBED_URL` | server, `put` | OpenAI-compatible embeddings endpoint |
| `PRIOMPT_EMBED_MODEL` | server, `put` | embedding model name |
| `PRIOMPT_EMBED_KEY` | server, `put` | API key for the embeddings endpoint |
| `PRIOMPT_REDIS_URL` | server | Redis URL for a shared L2 cache |
| `PRIOMPT_JWKS_URL` | server | priompt-auth `/jwks` URL; accept its short-lived JWTs |
| `PRIOMPT_ENCRYPTION_KEY` | server, `put`, `backup` | base64 32-byte key; AES-256-GCM at-rest encryption |
| `PRIOMPT_GIT_TOKEN` | `promptctl` (cli repo) | token for HTTPS git remotes |

CLI flags are listed under [the `priompt` command](#the-priompt-command);
[docker-compose.yml](docker-compose.yml) shows a full `serve` invocation, and
[docker-compose.tei.yml](docker-compose.tei.yml) adds a real embedding server
(HuggingFace TEI) for the semantic diff.

## gRPC API reference

Defined in the **proto** repo (`proto/priompt/v1/prompt.proto`) — the single
source of truth for the contract:

```proto
service PromptService {
  rpc GetPrompt(GetPromptRequest)         returns (GetPromptResponse);
  rpc DiffPrompt(DiffPromptRequest)       returns (DiffPromptResponse);   // diff stored vs. an edit
  rpc PublishPrompt(PublishPromptRequest) returns (PublishPromptResponse);
  rpc History(HistoryRequest)             returns (HistoryResponse);      // commit log of a branch
  rpc CreateBranch(CreateBranchRequest)   returns (CreateBranchResponse);
  rpc MergeBranch(MergeBranchRequest)     returns (MergeBranchResponse);
  rpc DiffCommits(DiffCommitsRequest)     returns (DiffPromptResponse);   // diff any two commits
  rpc SetBranch(SetBranchRequest)         returns (SetBranchResponse);    // rollback / pin a branch
  rpc ListPrompts(ListPromptsRequest)     returns (ListPromptsResponse);  // browse a URI prefix as a tree
}
```

`GetPromptRequest` takes an optional `ref` (branch name or commit hash) to fetch
a pinned version instead of the served HEAD; the response then includes the
`commit_hash` served.

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
| `PermissionDenied` | the token's org scope does not match the URI's org, or a read-only token tried to write |
| `ResourceExhausted` | over the per-org rate limit |
| `DataLoss` | a stored prompt failed serve-time validation |
| `Internal` | storage or lookup error |

## For developers of Priompt itself

Requirements: **Go 1.25+**. To change the API contract you also need **buf**
(<https://buf.build/docs/installation> — remote plugins, no `protoc` needed).

```sh
make build        # go build -> ./priompt(.exe)
make test         # go test ./...
make gen          # regenerate Go stubs in the sibling proto repo (after a .proto change)
```

The `.proto` contract and its generated Go stubs live in the sibling **proto**
repo (module `priomptproto`) — the single source of truth for server, CLI, and
SDKs. After editing it, `buf generate` there, then `buf generate` in
python-sdk and `npm run sync-proto` in js-sdk.

Project layout:

```text
(module priomptproto, proto repo)  Shared: the service contract + Go stubs, the JWT claims
                                   contract, the validate rules, and the semdiff engine.
(module priomptdb, db-adapters)    SQLite/Postgres storage: prompts, commits, refs, migrations.

internal/auth/auth.go              The gatekeeper: a pluggable Provider interface (static tokens
                                   and priompt-auth JWTs ship in core) + org scoping.
internal/server/server.go          gRPC handlers.
internal/server/observability.go   Metrics, audit-log, rate-limit interceptors.
internal/server/cache.go           In-process and Redis L2 caches.
internal/pubsub/pubsub.go          Embedded NATS publisher + subscriber.

cmd/priompt/main.go                Server + operational CLI.
```

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
