# cfdo

**A single-binary CLI for operating Cloudflare Durable Objects.** Scaffold a project, upload
the worker, see what is deployed, and back up or restore the storage inside every object.
No Node, no `wrangler`, no `node_modules`.

```sh
cfdo init                 # store credentials once
cfdo create chat-room     # scaffold a worker + Durable Object class
cd chat-room && cfdo upload
cfdo backup               # every object's storage, to disk
```

- [Why cfdo](#why-cfdo)
- [Install](#install)
- [Quick start](#quick-start)
- [Commands](#commands)
- [How objects are discovered](#how-objects-are-discovered)
- [Storage fidelity](#storage-fidelity)
- [First deploy on a fresh account](#first-deploy-on-a-fresh-account)
- [Files](#files) · [Environment](#environment) · [Tests](#tests)

## Why cfdo

| Command | What it does |
|---|---|
| `cfdo init` | record credentials in `~/.cfdo/settings.json` |
| `cfdo create <name>` | scaffold a project (worker, config, Claude Code skill) |
| `cfdo list` | every Durable Object namespace on the account |
| `cfdo upload` | push the worker and apply class migrations |
| `cfdo status [<script> \| --ns <ns>]` | deployment, namespace, object counts, admin health |
| `cfdo backup` | dump every object's storage to a directory |
| `cfdo restore <dir>` | load a backup back into the objects |
| `cfdo delete <script> \| --ns <ns>` | delete a worker and all its Durable Object data |
| `cfdo plugin add\|update\|list` | vendor libraries such as iroh-wasm, with their skill |

- **Backups that actually contain your data**, with checksums and a manifest, restorable
  in merge or point-in-time replace mode.
- **Migrations handled for you.** cfdo tracks which tag Cloudflare has applied and sends the
  right `old_tag → new_tag` step.
- **Works from anywhere.** `list`, `status <script>` and `delete` need no project directory.
- **Static assets** served next to the worker from a `public/` directory.
- **A generated Claude Code skill** in every project, so an agent knows how to build on it.

## The one thing to know about backups

**Cloudflare exposes no server-side API that can read Durable Object storage.** The DO API
can list namespaces and object ids; that is all. Anything claiming to back up DO *contents*
has to run code inside the object.

So `cfdo create` scaffolds a worker with a `/__cfdo/` admin route guarded by a shared
secret, and `backup`/`restore` drive it over HTTPS. Delete those routes from the generated
`worker.mjs` and backup stops working; everything else keeps working.

## Install

Requires Go 1.26 or newer.

```sh
git clone https://git.kat56.de/ulrich/cfdo.git
cd cfdo
go build -o cfdo .
sudo install cfdo /usr/local/bin/   # or anywhere on your $PATH
```

Shell completion is built in: `cfdo completion bash|zsh|fish|powershell --help`.

You need a Cloudflare API token with **Workers Scripts:Edit** and **Account Settings:Read**.

## Quick start

```sh
cfdo init                           # prompts, writes ~/.cfdo/settings.json (0600)

cfdo create chat-room               # scaffolds ./chat-room, uses the shared admin secret
cd chat-room

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

**Admin secrets are shared by default.** Every script uses the global `cfdo_secret` unless
it has its own: `cfdo create --custom-secret` or `init --script <name>` records one under
`scripts.<name>`, and a bare `--secret` sets the shared one. Re-running `init` updates what you pass and leaves the rest alone; `--force`
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
Uses the shared `cfdo_secret` from `~/.cfdo/settings.json` as the admin secret, generating
a 32-byte one there on first use (or adopting an exported `CFDO_SECRET`). With
`--custom-secret` it instead generates a secret for this script alone, saved as
`scripts.<name>.cfdo_secret`.

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
| `--plugin <dir>` | vendor a plugin (repeatable), see [`cfdo plugin`](#cfdo-plugin-addupdatelist) |
| `--custom-secret` | generate an admin secret for this script instead of using the shared one |
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

Uploads the worker and works out the migration. Starting at `main_module`, it follows every
relative import (`import`, `export … from`, `import()`) and uploads exactly the modules that
are reached: `.js`/`.mjs` as ES modules, `.wasm` as compiled WebAssembly, `.txt`/`.html` as
text, `.bin` as data. Files nothing imports are not sent. `--dry-run` lists them. Durable Object migrations are tag-based:
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

**Static files.** Set `"assets": "public"` in `cfdo.json` and every upload also publishes
that directory through Cloudflare's static assets: `public/index.html` is served at `/`,
`public/css/site.css` at `/css/site.css`, with no worker code. A request that matches no
file falls through to the worker, so API and admin routes keep working. The worker can
also read files through the `ASSETS` binding (`env.ASSETS.fetch(request)`).

- Only changed files are sent; Cloudflare keeps the rest by content hash.
- Hidden files and directories (`.DS_Store`, `.git/`) are skipped, files over 25 MiB are
  refused, and so is anything under `public/__cfdo/`, which would shadow the admin routes.
- `--no-assets` uploads the worker alone. Cloudflare then serves **no** static files until
  the next full upload.

### `cfdo status`

Whether the script is deployed, its live bindings and compatibility date, the namespace id,
object counts from both sources, the worker URL, and whether the admin route answers.
`--objects` lists every id, `--json` emits the same data, `--no-ping` skips the live check.

Because it compares the local migration tag against what Cloudflare reports, this is the
command to run when an upload fails with a tag mismatch.

```sh
cfdo status                 # the project in the nearest cfdo.json
cfdo status chat-room       # any script on the account, from anywhere
cfdo status --ns <ns>       # the script owning a namespace (id or name)
```

With a script or `--ns` no `cfdo.json` is needed: the account comes from `--account`,
the environment or `~/.cfdo/settings.json`, every namespace the script defines is shown,
and the admin check uses the script's recorded secret over workers.dev. There is no local
migration state to compare against in this form.

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

### `cfdo delete <script> | --ns <namespace>`

Deletes a worker **and every Durable Object namespace it defines, with all their data**.
`--ns` takes a namespace id or name and deletes the script that owns it — Cloudflare
removes a script's namespaces together, so the script's other namespaces go too. The
command lists everything that will be removed before asking.

```sh
cfdo delete chat-room           # prompts: type the script name to confirm
cfdo delete --ns <ns> --yes     # scripted
```

- Needs no `cfdo.json`; `--account` picks a different account.
- Without a terminal it refuses unless `-y`/`--yes` is given.
- Run from inside the matching project, it also resets `.cfdo/state.json` so the next
  `cfdo upload` recreates the classes from scratch.
- Per-script secrets in `~/.cfdo/settings.json` are left in place.

This cannot be undone. Run `cfdo backup` first.

### `cfdo plugin add|update|list`

A plugin is a library for the worker, such as iroh-wasm, that ships a
`cfdo-plugin.json` at its root:

```json
{ "name": "iroh-wasm", "version": "2026-10-02",
  "worker": "dist/iroh-worker", "entry": "iroh.js", "skill": "SKILL.md" }
```

```sh
cfdo create chat-room --plugin ~/src/iroh-wasm   # at scaffold time
cfdo plugin add ~/src/iroh-wasm                  # or later, in a project
cfdo plugin update [iroh-wasm]                   # re-copy from the recorded source
cfdo plugin list
```

- The files in `worker` are copied to `plugins/<name>/`. The worker imports the entry
  module, e.g. `import { IrohEndpoint } from "./plugins/iroh-wasm/iroh.js"`, and `upload`
  ships it along with whatever it imports in turn.
- The plugin's skill goes to `.claude/skills/<name>/SKILL.md`, with a note added on where
  the plugin lives in this project.
- `cfdo.json` records the source, version and a sha256 of what was copied. Uploads always
  send the vendored copy, so a plugin that changes upstream changes nothing until you run
  `plugin update`, which reports whether anything changed.
- `plugins/<name>/` and the plugin's skill belong to cfdo and are replaced on update. Keep
  your own code in `worker.mjs`.

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
| `plugins/<name>/` | vendored plugin files, with their skill in `.claude/skills/<name>/` — commit them |
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
