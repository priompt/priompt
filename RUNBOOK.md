# Priompt — End-to-End Test Runbook

**Every service, every feature, exercised the way a developer actually uses it.**
This document is the full-system verification suite for the Priompt family:
build everything, run everything, break everything that should refuse to work,
and stress what should scale. Every output shown below is **real, captured from
an actual run** (Windows 11, Go 1.26, Python 3.13, Node 24 — re-run in full
**2026-07-23** on the restructured layout; first captured 2026-07-21).

The system under test — seven repos:

| Repo | Ships | Role in this runbook |
| --- | --- | --- |
| **priompt** | `priompt` server binary | The system under test everywhere |
| **proto** | Go module `priomptproto` | The shared contract + engines under every suite; T16 — its own test suite |
| **auth** | `priompt-auth` binary | T13 — enterprise token issuance |
| **cli** | `promptctl` binary | T14 — authoring workflow |
| **python-sdk** | pip package `priompt` | T10, T11, T15 — client + versioning API + load |
| **js-sdk** | npm `priompt-client` | T12 — Node client |
| **db-adapters** | Go module `priomptdb` | T16 — its own test suite |

> **Restructure note.** Between the two runs the family was restructured: the
> shared **proto** repo now holds the gRPC contract, JWT claims, validation
> rules, and semdiff engine (all previously copied per-repo); the server
> imports `priomptdb` instead of carrying its own store; and core auth became
> a pluggable `Provider` interface. The 2026-07-23 re-run below is the proof
> the restructure preserved behavior end to end — several content hashes
> (T1's `22e5cc10ac0f`, T20's `e345dcf74d01`) reproduce the original run
> exactly. Details in `GAP-ASSESSMENT-2.md` in the family checkout.

Result: **22/22 suites pass** — including the infrastructure suites (Postgres,
Redis, TLS/mTLS, the Docker image, real TEI embeddings, and SSO against a live
Keycloak IdP), all run in Docker. The scorecard is at the
[bottom](#scorecard); the only thing not exercised is registry publishing
(Docker/PyPI/npm), which is blocked on name placeholders.

## Setup

```sh
cd priompt      && go build -o priompt.exe ./cmd/priompt
cd ../auth      && go build -o priompt-auth.exe .
cd ../cli       && go build -o promptctl.exe .
cd ../python-sdk && python -m venv venv && venv/Scripts/pip install -e . pytest
cd ../js-sdk    && npm install
```

The Go repos reference the sibling `proto` and `db-adapters` checkouts via
`replace` directives (until the modules are published), so clone the family
side by side — as the `cd ../` steps above already assume.

All server tests below use `127.0.0.1:18443` (gRPC), `:18444` (auth), `:14222`
(NATS), `:12112` (metrics) — any free ports work.

---

## T1 — Validation gate (writes cannot store garbage)

A template that uses an undeclared slot must be rejected *and write nothing*:

```sh
echo "Hi {name}, welcome to {org}!" > welcome.txt
./priompt put -db t1.db -uri priompt://acme/onboarding/welcome -file welcome.txt -slot name -slot org
# stored priompt://acme/onboarding/welcome (22e5cc10ac0f)

./priompt put -db t1.db -uri priompt://acme/bad/x -file welcome.txt -slot name
# validation failed: template uses undeclared slots: org
# exit=1                                                          ✅
```

## T2 — Encryption at rest (a stolen DB file is useless)

```sh
export PRIOMPT_ENCRYPTION_KEY=$(head -c32 /dev/urandom | base64)
./priompt put -db enc.db -uri priompt://acme/secret/x -file welcome.txt -slot name -slot org

grep -c "welcome to" enc.db          # 0  — no plaintext on disk        ✅
grep -c "enc1:" enc.db               # >0 — ciphertext marker present   ✅
./priompt backup -db enc.db -out dump.jsonl
grep -c "welcome to" dump.jsonl      # 1  — backup decrypts WITH the key ✅
```

## T3 — Backup / restore roundtrip (portable, idempotent)

```sh
./priompt backup  -db t1.db    -out snap.jsonl     # backed up 2 prompts
./priompt restore -db fresh.db -in  snap.jsonl     # restored 2 prompts
./priompt backup  -db fresh.db -out snap2.jsonl
diff <(sort snap.jsonl) <(sort snap2.jsonl)        # identical            ✅
```

The JSONL format is DB-agnostic — this same pair of commands is the
SQLite→Postgres migration path.

## T4 — Schema migrations

```sh
./priompt migrate -db t1.db
# schema up to date (version 4)                                          ✅
```

## T5 — Static tokens: org scoping, read/write grants, expiry

```text
# tokens.txt
admin-rw               rw
acme-read     acme
acme-author   acme     rw
stale-key     acme  2020-01-01 rw
```

```sh
./priompt serve -addr 127.0.0.1:18443 -db t1.db -tokens-file tokens.txt ...
```

| Attempt | Observed |
| --- | --- |
| `admin-rw` lists everything | both prompts returned ✅ |
| `acme-read` lists `priompt://acme/…` | returns acme prompts ✅ |
| `acme-read` lists `priompt://other/…` | `PermissionDenied: token not authorized for org "other"` ✅ |
| `acme-read` (read-only) publishes | `PermissionDenied: token is read-only` ✅ |
| `stale-key` (expired 2020) does anything | `Unauthenticated: token expired` ✅ |

## T6 — Pub/sub: subscribers get the change *and* the safety verdict

Terminal 1 subscribes, terminal 2 publishes:

```sh
./priompt watch -uri priompt://acme/onboarding/welcome -nats-url nats://127.0.0.1:14222
./priompt publish -uri priompt://acme/onboarding/welcome -file v2.txt -slot name -slot org
```

Observed on the watcher, ~1s after the publish:

```text
priompt://acme/onboarding/welcome updated -> 4146f71b692c… [localized tweak]   ✅
```

The `[localized tweak]` verdict is the semantic diff, computed server-side at
publish time — this is what lets an agent auto-reload safe changes and hold
`[structural]` ones for review.

## T7 — Semantic Propagation Diff: both verdicts, provoked on purpose

A wording rewrite → **localized tweak**:

```sh
./priompt diff -uri priompt://acme/onboarding/welcome -file risky.txt -addr 127.0.0.1:18443
# change @ new lines 1-1 (old 1-1): replace
#   Signal 2 (point delta): 0.667
#   Signal 3 up:   ±2=0.667 (flat)
#   Signal 3 down: ±2=0.667 (flat)
#   => localized tweak                                                   ✅
```

A meaning inversion ("welcome" → "access DENIED"), via the Python client →
**structural**:

```python
d = client.diff("priompt://acme/onboarding/welcome", "Hi {name}. Access to {org} is DENIED.")
d.changes[0].classification    # 'structural'                            ✅
```

(Ran with the offline lexical embedder — no model server needed. Point
`-embed-url` at a real embeddings endpoint for meaning-level judgment.)

## T8 — Rate limiting (per-org token bucket)

Server started with `-rate-limit 3 -rate-burst 3`, then 5 rapid calls as org
`acme`:

```text
call 1 exit=0
call 2 exit=0
call 3 exit=0
call 4 exit=1   <- burst of 3 drained
call 5 exit=1
# ResourceExhausted: rate limit exceeded for org "acme"                  ✅
```

## T9 — Metrics (Prometheus)

```sh
curl -s http://127.0.0.1:12112/metrics | grep ^priompt_requests_total
```

```text
priompt_requests_total{code="OK",method=".../DiffPrompt"} 1
priompt_requests_total{code="OK",method=".../ListPrompts"} 2
priompt_requests_total{code="OK",method=".../PublishPrompt"} 2
priompt_requests_total{code="PermissionDenied",method=".../ListPrompts"} 1
priompt_requests_total{code="PermissionDenied",method=".../PublishPrompt"} 1
priompt_requests_total{code="Unauthenticated",method=".../ListPrompts"} 1    ✅
```

Even the denials from T5 are counted, labeled by gRPC code.

## T10 — Python SDK (`get` / `list` / `diff`)

```python
from priompt import PromptClient
c = PromptClient(host="127.0.0.1:18443")
c.get("priompt://acme/onboarding/welcome")
# template='Hi {name}, welcome back to {org}!…' slots=['name','org'] hash=4146f71b692c ✅
[e.uri for e in c.list("priompt://acme/")]
# ['priompt://acme/onboarding/welcome', 'priompt://acme/support/agent', …]             ✅
c.diff(uri, "Hi {name}. Access to {org} is DENIED.").changes[0].classification
# 'structural'                                                                          ✅
```

(`subscribe()` needs `pip install nats-py`; the notification path itself is
verified in T6.)

## T11 — Full versioning lifecycle over the raw gRPC API

Branch work is invisible until merged; merges record two parents; rollback is
a pointer move; pinning fetches an exact point in history. All via the Python
generated stubs — i.e. what *any* language sees:

```text
publish "Base {x}" onto main
CreateBranch feat2 from main
publish "Feature {x}" onto feat2
GetPrompt            -> "Base {x}"      (branch work invisible)          ✅
MergeBranch feat2 -> main
GetPrompt            -> "Feature {x}"                                    ✅
History main         -> [('merge feat2 into main', parent2=True), ('base', False)]  ✅
SetBranch main -> <base commit>   (rollback)
GetPrompt            -> "Base {x}"                                       ✅
GetPrompt ref=feat2  -> "Feature {x}" + commit 4f6d22c8e332   (pinning)  ✅
```

## T12 — JavaScript SDK (`get` / `publish` / read-back)

```js
const c = new PromptClient();          // config via PRIOMPT_URL
await c.get("priompt://acme/onboarding/welcome");   // template + hash    ✅
await c.publish("priompt://acme/js/hello", "Hallo {wer}!", ["wer"]);
await c.get("priompt://acme/js/hello"); // "Hallo {wer}!"  read-back      ✅
```

## T13 — priompt-auth: the enterprise credential lifecycle

Setup: `init` → `gen-secret` → `serve`; the prompt server runs with **both**
`-tokens-file` and `-auth-jwks-url` (coexistence):

| Attempt | Observed |
| --- | --- |
| Issue a token with the right secret | 200 + EdDSA JWT (`expires_in: 900`) ✅ |
| Issue with a wrong secret | `401 {"error":"invalid_client"}` ✅ |
| rw JWT lists + publishes | both succeed — full round trip via `/jwks` ✅ |
| read-only JWT publishes | `PermissionDenied: token is read-only` ✅ |
| org-scoped JWT on its own org | listed ✅ — same scoping engine as static tokens |
| Garbage JWT | `Unauthenticated: invalid or missing token` ✅ |
| **TTL expiry**: token issued with `-ttl 2s`, used at t=0 | works ✅ |
| …same token at t=3s | `Unauthenticated` — short-lived creds die on their own ✅ |
| Static token on the same server, after all of the above | still works — gradual migration holds ✅ |

The OIDC/SSO exchange (corporate IdP → Priompt JWT, group mapping, issuer/
audience/expiry checks) is covered by the auth repo's test suite against an
in-test fake IdP — `go test ./...` in `auth/`, see T16.

## T14 — promptctl: the authoring loop

```sh
git init prompts-repo && cd prompts-repo
echo "You are a support agent for {org}. Be concise and kind." > acme/support/agent.prompt

promptctl commit -m "first prompt"     # committed f1a28af63fea           ✅
```

The validation gate (an invalid prompt blocks the **whole** commit):

```sh
: > acme/support/empty.prompt          # empty template = invalid
promptctl commit -m "should fail"
# 1 invalid prompt(s); nothing committed                                  ✅
```

> Note promptctl reads slots *from* the template's `{placeholders}`, so
> "undeclared slot" errors can't happen here by construction — that gate
> belongs to the server write path (T1). promptctl's gate catches what files
> can get wrong: empty templates, empty slot names.

Semantic diff before shipping, lineage, and publish:

```sh
promptctl diff                          # => minor edit (report per hunk)      ✅
promptctl commit -m "tighten policy"
promptctl log acme/support/agent.prompt
# 1b23fe84 tighten policy
# f1a28af6 first prompt                                                   ✅
promptctl publish -server 127.0.0.1:18443
# published priompt://acme/support/agent (5804155797f8)                   ✅
```

File path → URI mapping confirmed: `acme/support/agent.prompt` arrived as
`priompt://acme/support/agent`.

## T15 — Stress

All against a single-node SQLite server on a laptop — the floor, not the
ceiling:

| Load | Observed |
| --- | --- |
| 100 sequential publishes (write path: validate + commit DAG + serve-copy per write) | **0.52s — 192 writes/s** ✅ |
| `list` after the burst | all 100 stored, none lost ✅ |
| 200 parallel `get`s, 32 threads | **0.03s — ~6,350 reads/s**, 200/200 correct ✅ |
| 1,000 `get`s, L1 client cache (`cache_ttl=30`) vs network | **<1ms vs 173ms — ~1,240× faster** ✅ |

## T16 — The per-repo test suites (the foundation under all of the above)

Re-captured 2026-07-23, after the restructure (see the layout note up top):

```text
proto        go test ./...   ok: semdiff, validate — the shared engines        ✅
priompt      go test ./...   ok: cmd/priompt, internal/{auth,pubsub,server}    ✅
auth         go test ./...   ok: minting, client_credentials, OIDC exchange vs fake IdP  ✅
cli          go test ./...   ok: promptctl                                     ✅
python-sdk   pytest -q       14 passed                                         ✅
db-adapters  go test ./...   ok: store (SQLite live), crypt                    ✅
js-sdk       node --check + construct client + load proto                      ✅
```

## T17 — TLS and mTLS

Self-signed CA + server cert (SAN `localhost/127.0.0.1`) + client cert via
openssl, then:

```sh
./priompt serve ... -tls-cert server.pem -tls-key server.key                    # TLS
./priompt serve ... -tls-cert server.pem -tls-key server.key -client-ca ca.pem  # mTLS
```

| Attempt | Observed |
| --- | --- |
| TLS client with pinned CA (`-tls -ca-cert ca.pem`) | listing served ✅ |
| Plaintext client against the TLS server | connection closed by server ✅ |
| mTLS client presenting `client.pem`/`client.key` | listing served ✅ |
| mTLS client with **no** certificate | refused at the TLS layer, before auth ✅ |

## T18 — PostgreSQL backend (Docker `postgres:16-alpine`)

The same binary, one flag changed — and the SQLite snapshot from T3 migrates
straight in:

```sh
./priompt migrate -db postgres://postgres:pw@127.0.0.1:15432/prompts
# schema up to date (version 4)                                          ✅
./priompt restore -db postgres://... -in snap.jsonl
# restored 2 prompts        <- SQLite -> Postgres migration, two commands ✅
./priompt serve -db postgres://...
```

Full versioning lifecycle re-run on Postgres:

```text
history: [('merge f into main', parent2=True), ('base', False)]          ✅
rollback -> "PG base {x}"                                                ✅
```

## T19 — Shared Redis L2 cache (Docker `redis:7-alpine`)

```sh
./priompt serve ... -redis-url redis://127.0.0.1:16379/0 -cache-ttl 60s
```

After two client `get`s, the cache is observable inside Redis itself:

```text
$ redis-cli KEYS '*'
priompt:cache:priompt://acme/onboarding/welcome
$ redis-cli TTL ...          -> 60                                       ✅
```

Publish a new version → the key is **gone** (write-through invalidation):

```text
$ redis-cli KEYS '*' | grep -c onboarding   -> 0                         ✅
```

## T20 — The shipped Docker image

`docker compose up --build` builds the multi-stage image (static Go binary on
alpine). Run and poke it from the outside:

```text
container log: seeded demo prompt priompt://acme/onboarding/welcome
               priompt serving on :8443 (tls=false ... nats=127.0.0.1:4222)

python client -> get('priompt://acme/onboarding/welcome')
               "Hi {name}, welcome to {org}!"  hash e345dcf74d01          ✅
```

(On this machine the compose default ports were held by a still-running
container from the pre-split repo — the image test ran on an alternate port
mapping instead. Worth knowing: old deployments keep working through a split.
Still true on the 2026-07-23 re-run: the same pre-split container held port
2112, the image built from the new parent-directory context, and the fetched
hash reproduced exactly.)

## T21 — Real embeddings (HuggingFace TEI, `BAAI/bge-small-en-v1.5`)

TEI in Docker, server pointed at it with `-embed-url`/`-embed-model`. The
verdicts sharpen visibly compared to the offline lexical embedder (T7):

A synonym swap ("politely" → "courteously") in a 5-line prompt:

```text
Signal 2 (point delta): 0.025    <- real model sees near-zero meaning shift
=> minor edit                                                            ✅
```

A policy inversion ("follow the refund policy" → "refuse immediately and
close the ticket without escalation"):

```text
Signal 2 (point delta): 0.261    <- 10x the synonym's delta
Signal 3 up: ±2=0.085 ±4=0.051 (boundary)  down: ±2=0.167 (boundary)
=> structural                                                            ✅
```

Same edit type, honestly different magnitudes — the model scores the synonym
as near-zero and the inversion an order of magnitude larger, with a ripple
that reaches the prompt's boundary and earns the **structural** flag. Exactly
what the propagation signals are for. (The lexical embedder in T7 can only
score word overlap; this is scoring meaning. Verdicts are input-exact: the
2026-07-21 run's 5-line prompt scored its inversion 0.212 with a flat ripple
— `localized tweak` — while this prompt's inversion ripples to the boundary.)

## T22 — SSO against a real IdP (Keycloak 26 in Docker)

Not a mock: a real Keycloak with realm `corp`, public client `priompt`
(direct-access grants), user `sujal` in group `acme-authors`, and a
group-membership protocol mapper emitting a `groups` claim.

```sh
./priompt-auth serve -oidc-issuer http://127.0.0.1:18085/realms/corp \
  -oidc-audience priompt -groups-file groups.txt        # acme-authors acme rw
./priompt serve ... -auth-jwks-url http://127.0.0.1:18450/jwks
```

The four-step round trip, observed:

```text
1. sujal logs in at Keycloak            -> RS256 ID token
2. exchange at priompt-auth             -> verified (sig via Keycloak JWKS,
   issuer, audience, expiry), groups mapped:
   priompt claims: {'sub': 'd5c6cf8b-…', 'org': 'acme', 'rw': True, 'iss': 'priompt-auth'}
3. that token against the Priompt server:
   list    -> priompt://acme/onboarding/welcome                             ✅
   publish -> published priompt://acme/sso/keycloak — subscribers notified  ✅
4. user "intern" (no mapped group) exchanges:
   HTTP 403 {"error":"access_denied"}                                    ✅
```

End to end: corporate identity → group → org-scoped, write-granted,
15-minute Priompt credential — with the prompt server never once calling
either Keycloak or priompt-auth on the request path.

(Keycloak 26 gotcha for re-runners: users created via the admin API need
email + first/last name set, or direct grants fail with
`invalid_grant: Account is not fully set up`.)

---

## Scorecard

| # | Suite | Verdict |
| --- | --- | --- |
| T1 | Validation gate on writes | ✅ |
| T2 | AES-256-GCM encryption at rest | ✅ |
| T3 | Backup/restore roundtrip | ✅ |
| T4 | Schema migrations | ✅ |
| T5 | Static tokens: scoping, rw, expiry | ✅ |
| T6 | Pub/sub with diff verdict | ✅ |
| T7 | Semantic diff: tweak AND structural | ✅ |
| T8 | Per-org rate limiting | ✅ |
| T9 | Prometheus metrics | ✅ |
| T10 | Python SDK | ✅ |
| T11 | Branch / merge / history / rollback / pin | ✅ |
| T12 | JavaScript SDK | ✅ |
| T13 | priompt-auth lifecycle incl. TTL death | ✅ |
| T14 | promptctl authoring loop | ✅ |
| T15 | Stress: writes, parallel reads, cache | ✅ |
| T16 | All per-repo unit/integration suites | ✅ |
| T17 | TLS + mTLS (4 cases) | ✅ |
| T18 | PostgreSQL backend + SQLite migration | ✅ |
| T19 | Redis L2 cache + write-through invalidation | ✅ |
| T20 | The shipped Docker image | ✅ |
| T21 | Real embeddings (TEI) sharpen the diff | ✅ |
| T22 | SSO via live Keycloak IdP, end to end | ✅ |

## Not run here

One item remains, deliberately excluded:

| What | Why |
| --- | --- |
| Registry publishing (GHCR image push, PyPI, npm) | Blocked on the name placeholders in `release.yml`, `pyproject.toml`, and `package.json` — pick the public names, tag `v*`, and the existing release workflows do the rest |

## Re-running this runbook

Every command above is copy-pasteable. Suites T1–T4 need no server; T5–T12
need one `priompt serve`; T13 adds `priompt-auth serve`; T14 runs from any git
repo; T17 needs openssl; T18–T22 need Docker Desktop (postgres:16-alpine,
redis:7-alpine, text-embeddings-inference:cpu-1.6, keycloak:26.0 — pulled
automatically). Or start with the unit suites (T16) — they need nothing but
the toolchains and finish in under a minute per repo.
