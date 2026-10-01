# cfdo

A single-binary CLI for operating Cloudflare Durable Objects: scaffold a project, upload
the worker, see what exists, and back up or restore object storage.

Go standard library only — no dependencies, no `node_modules`.

```sh
go build -o cfdo .
```

| Command | What it does |
|---|---|
| `cfdo init` | record credentials in `~/.cfdo/settings.json` |
| `cfdo create <name>` | scaffold a project (worker, config, Claude Code skill) |
| `cfdo list` | every Durable Object namespace on the account |
| `cfdo upload` | push the worker and apply class migrations |
| `cfdo status` | deployment, namespace, object counts, admin health |
| `cfdo backup` | dump every object's storage to a directory |
| `cfdo restore <dir>` | load a backup back into the objects |

## The one thing to know about backups

**Cloudflare exposes no server-side API that can read Durable Object storage.** The DO API
can list namespaces and object ids; that is all. Anything claiming to back up DO *contents*
has to run code inside the object.

So `cfdo create` scaffolds a worker with a `/__cfdo/` admin route guarded by a shared
secret, and `backup`/`restore` drive it over HTTPS. Delete those routes from the generated
`worker.mjs` and backup stops working; everything else keeps working.

## Quick start

```sh
cfdo init                           # prompts, writes ~/.cfdo/settings.json (0600)

cfdo create chat-room               # scaffolds ./chat-room, prints a secret
cd chat-room
cfdo init --script chat-room --secret <that secret>

cfdo upload                         # uploads worker.mjs, applies the v1 migration
cfdo status                         # deployment, namespace, objects, admin health
cfdo backup                         # -> ./backups/chat-room-<timestamp>/
cfdo restore backups/chat-room-...  # merge by default; --mode replace wipes first
```

Environment variables work instead of `init` if you prefer; they always take precedence.

## Commands

### `cfdo init`

Records credentials in `~/.cfdo/settings.json` (mode 0600, in a 0700 directory). With no
flags it prompts, with terminal echo off for the token and secret. It verifies the token
against your account **before writing anything**, so a bad token fails with nothing saved.

```sh
cfdo init                                        # interactive
cfdo init --token ... --account ... --no-verify  # scripted
cfdo init --script my-do --secret ...            # secret for one project
cfdo init --script my-do --generate-secret       # generate one instead
cfdo init --show                                 # what is stored, and what resolves
```

**Admin secrets are per script.** Two projects on one account have two different secrets,
so `--script <name>` scopes a secret to that script and a bare `--secret` sets only a
fallback. Re-running `init` updates what you pass and leaves the rest alone; `--force`
re-prompts for values already set. Nothing ever prints a credential in full.

Resolution order, most specific first:

| Value | Order |
|---|---|
| API token | `CLOUDFLARE_API_TOKEN` → `cloudflare_api_token` |
| Account id | `CLOUDFLARE_ACCOUNT_ID` → `account_id` in `cfdo.json` → `cloudflare_account_id` |
| Admin secret | `CFDO_SECRET` → `scripts.<name>.cfdo_secret` → `cfdo_secret` |

### `cfdo create <script-name>`

Writes `cfdo.json`, a `worker.mjs` containing a Durable Object class plus the admin
routes, a `.gitignore`, and a Claude Code skill at `.claude/skills/<name>/SKILL.md`.
Prints a freshly generated 32-byte secret.

Two regions of `worker.mjs` are marked as yours — the class's `app()` method and the
default export's routing. The rest is cfdo machinery.

| Flag | Meaning |
|---|---|
| `--dir` | scaffold somewhere other than `./<name>` |
| `--class`, `--binding` | override the derived `ChatRoom` / `CHAT_ROOM` names |
| `--kv` | use the legacy key-value backend instead of SQLite-backed storage |
| `--compat-date` | worker compatibility date (default: today) |
| `--account` | account id to write into `cfdo.json` |
| `--no-skill` | do not write the Claude Code skill |
| `--force` | overwrite existing files |

The generated skill covers what Durable Objects can do, how to implement against this
class (storage, alarms, WebSockets, RPC), when a DO is the wrong tool, and the operational
rules for the project. Its storage sections are generated per backend, so a `--kv`
project's skill never documents a SQL API it does not have. Commit it.

### `cfdo list`

Every namespace on the account, with script, class, backend, object count and namespace id.
Needs no `cfdo.json`, so it works from anywhere — this is the "what exists here?" command.

```sh
cfdo list                 # all namespaces
cfdo list --script my-do  # one script's
cfdo list --objects       # with every object id
cfdo list --deep          # add each worker's own index count
cfdo list --json          # machine-readable
cfdo list --account <id>  # a different account
```

A namespace whose worker is not deployed is marked `(not deployed)`. `--deep` reaches each
worker's admin route, so it needs that script's secret recorded and the worker served on
workers.dev.

### `cfdo upload`

Uploads the module and works out the migration. Durable Object migrations are tag-based:
the class list is only sent on the first upload, and later uploads send `old_tag → new_tag`.
cfdo tracks what it has applied in `.cfdo/state.json`.

- Bump `migration_tag` in `cfdo.json` to push a migration.
- `--dry-run` prints the exact upload metadata (secret masked) and sends nothing.
- `--new-class`, `--deleted-class`, `--renamed-class old=new` for multi-class changes.
- `--no-secret` skips the `CFDO_SECRET` binding — backup and restore then stop working.
- `--tag <v>` applies a one-off tag without editing `cfdo.json`.
- `-c`/`--config <path>` points at a `cfdo.json` other than the nearest one up the tree (all
  project commands accept this).

The secret is uploaded as a `secret_text` binding on every upload, so the value you have
locally is always the one the worker checks.

**Deleting a class destroys its objects' data**, permanently. Back up first.

### `cfdo status`

Whether the script is deployed, its live bindings and compatibility date, the namespace id,
object counts from both sources, the worker URL, and whether the admin route answers.
`--objects` lists every id, `--json` emits the same data, `--no-ping` skips the live check.

Because it compares the local migration tag against what Cloudflare reports, this is the
command to run when an upload fails with a tag mismatch.

### `cfdo backup`

Discovers every object, exports each through the worker, and writes:

```
backups/chat-room-20260930T120000Z/
  manifest.json          # namespace, class, per-object sha256, any failures
  objects/<id>.json      # exactly what the object returned
```

`manifest.json` is written last, so its presence means the backup finished.

| Flag | Meaning |
|---|---|
| `-o`/`--output <dir>` | output directory (default: `./backups/<script>-<timestamp>`) |
| `--concurrency <n>` | objects to export in parallel (default 8) |
| `--continue-on-error` | record failures in the manifest instead of aborting |
| `--limit <n>` | stop after n objects, for a trial run |
| `--ids-file <path>` | extra object ids to export, one per line |
| `--include-empty` | also try objects the API reports as having no stored data |

### `cfdo restore <backup-dir>`

Verifies each file against its manifest checksum, then pushes it back.

- `--mode merge` (default) writes the backed-up keys over whatever is there now.
- `--mode replace` deletes the object's storage first — a true point-in-time restore.
- `--only <id>` restores a single object, `-y`/`--yes` skips the confirmation prompt.
- `--concurrency <n>` objects in parallel (default 4).
- `--skip-verify` skips the manifest checksum check — only for a backup you edited
  deliberately.

Restores are **not atomic across objects**: each is replaced independently, and a failure
partway leaves earlier objects already restored. Failures print to stderr and the command
exits non-zero.

## How objects are discovered

This is the part that bites, and the reason backup does not simply trust the API.

Cloudflare's object-listing endpoint is unreliable for SQLite-backed namespaces — the
default and recommended backend. Verified live: four objects holding data listed as **zero**
immediately after writing, and as **two** several minutes later. A backup that trusted it
would silently miss objects.

So the generated worker keeps its own registry, and `cfdo backup` unions three sources,
printing which one found what:

1. **The worker's index.** Every name the worker routes to is recorded in a `__cfdo_index`
   object, read back via `/__cfdo/list`. Immediate, but only knows objects routed by name
   through the generated code, and never lists itself.
2. **The Cloudflare listing API.** Complete for key-value namespaces. For SQLite it lags —
   and once it catches up it *also* counts the `__cfdo_index` object that the index omits.
3. **`--ids-file`**, one id per line, for ids neither source can know.

Neither source is a superset of the other, so `cfdo list --deep` shows both counts side by
side rather than pretending one is authoritative. If you route with `newUniqueId()`, your
app is the only thing that knows those ids — record them and pass `--ids-file`.

## Storage fidelity

DO storage holds structured-clonable values, which JSON cannot represent on its own. The
generated worker tags the types JSON would lose — `ArrayBuffer`, typed arrays, `Date`,
`Map`, `Set`, `BigInt`, `undefined` — so a round trip is lossless, including values that
happen to look like the tags themselves. SQLite-backed classes additionally get their user
tables dumped with schema and rows. Alarms are preserved.

## First deploy on a fresh account

Two things bite on an account that has never run a Worker:

- **No workers.dev subdomain.** `cfdo status` shows no worker URL and the admin route as
  unreachable. Open Workers & Pages in the dashboard once, or
  `PUT /accounts/{id}/workers/subdomain` with `{"subdomain":"yourname"}`. The name is
  account-wide, and the API only *creates* one — renaming needs the dashboard.
- **TLS on a new hostname takes a few minutes.** Until the certificate is issued, requests
  fail with `tls: handshake failure`. The script, its bindings and the namespace are
  already live at that point; only the public hostname lags.

If the worker is served on a route you already own, set `worker_url` in `cfdo.json` and
neither applies.

## Example

`examples/guestbook/` is a website served entirely from Durable Objects: one room per
object, messages in the object's own SQLite database, HTML rendered by the object. It is
the scaffold with `app()` and the routing replaced, so the admin routes still work and
`cfdo backup` captures the `messages` table with schema and rows. See its README.

## Files

| Path | Purpose |
|---|---|
| `~/.cfdo/settings.json` | your credentials, mode 0600 — **never commit** |
| `cfdo.json` | project config — commit it |
| `worker.mjs` | your Durable Object class plus the admin routes |
| `.claude/skills/<name>/SKILL.md` | project skill for Claude Code — commit it |
| `.cfdo/state.json` | applied migration tag, cached namespace id — **do not commit** |
| `backups/` | backup output |

If `.cfdo/state.json` is lost, `cfdo status` shows the tag Cloudflare has; set
`applied_migration_tag` to it by hand rather than re-running the initial migration.

## Environment

| Variable | Purpose |
|---|---|
| `CLOUDFLARE_API_TOKEN` | required by every command that touches the API |
| `CLOUDFLARE_ACCOUNT_ID` | overrides `account_id` in `cfdo.json` |
| `CFDO_SECRET` | shared secret for the worker admin routes |
| `CFDO_API_BASE` | point the client at a different API base (tests, gateways) |
| `CFDO_HOME` | directory holding `.cfdo/settings.json` (default: your home directory) |

The first three fall back to `~/.cfdo/settings.json` when unset.

The API token needs **Workers Scripts:Edit** and **Account Settings:Read**. Note that an
account-scoped token fails `/user/tokens/verify` with "Invalid API Token" while working
perfectly — use `/accounts/{id}/tokens/verify` instead, which is what `cfdo init` does.

## Tests

```sh
go test ./...
```

Covers the backup/restore round trip against a fake worker and a paginating fake
Cloudflare API, discovery when the API lists nothing, `--ids-file` merging without
duplicates, checksum tamper detection, secret mismatch, migration planning, credential
resolution order, settings file permissions, credential masking, skill generation per
storage backend, flag permutation, and error propagation.
