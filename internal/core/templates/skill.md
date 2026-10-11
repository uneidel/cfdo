---
name: __SCRIPT_NAME__
description: Work on the __SCRIPT_NAME__ Cloudflare Durable Object project - what Durable Objects can do, how to implement features on the __CLASS_NAME__ class (storage, alarms, WebSockets, RPC), and how to operate it with the cfdo CLI (upload, migrations, status, backup, restore). Use when adding behaviour to this Durable Object, deciding whether a DO is the right fit, changing its classes or migration tag, or touching its /__cfdo/ admin routes and backups.
---

# __SCRIPT_NAME__ (Durable Object)

| | |
|---|---|
| Script | `__SCRIPT_NAME__` |
| Class | `__CLASS_NAME__` (__STORAGE__) |
| Binding | `env.__BINDING__` |
| Entry point | `worker.mjs` |
| Project config | `cfdo.json` — commit it |
| Static files | the directory named by `assets` in `cfdo.json` (unset = none) |
| Applied migration | `.cfdo/state.json` — do not commit |

Two marked regions in `worker.mjs` are yours: the class's `app()` method (`your code`) and
the default export's routing (`your routing`). Everything else is cfdo machinery.

## What a Durable Object gives you

- **One instance per id, globally.** `idFromName("room-7")` always reaches the same object
  from anywhere. That object is the single authority for its own data.
- **Single-threaded execution.** Requests to one object run one at a time, so
  read-modify-write needs no transaction, no lock, and cannot race. This is the main
  reason to choose a DO over a Worker plus a database.
- **Private, co-located storage.** Each object has its own storage, sitting next to the
  code that uses it, so reads are local rather than a network round trip.
- **Alarms.** `ctx.storage.setAlarm(time)` wakes the object later and runs its `alarm()`
  handler — scheduled work without a cron worker, and it survives eviction.
- **WebSockets with hibernation.** `ctx.acceptWebSocket(ws)` plus `webSocketMessage()` /
  `webSocketClose()` handlers let an object hold many long-lived connections while being
  evicted from memory between messages, so idle connections cost nothing.
- **RPC.** Because the class extends `DurableObject`, a public method can be called
  directly on the stub: `await env.__BINDING__.get(id).addMessage(text)`. Cleaner than
  encoding calls into `fetch` URLs. `fetch` is still needed for WebSockets and for
  forwarding a real request.
- **Automatic consistency.** Cloudflare's input and output gates mean a storage write is
  durable before a response is delivered, without explicit flushes.

__STORAGE_CAPABILITIES__

## When a DO is the wrong answer

- **Read-heavy global data.** All traffic for an id funnels through one instance in one
  location. Readers on other continents pay the latency. Use KV or Cache for that.
- **A single hot object.** One object is one thread. Sharding across many ids scales;
  funnelling everything into one does not. If a design has a "main" object that every
  request touches, that object is the ceiling.
- **Cross-object transactions.** There is no atomic write across two objects. If two
  pieces of data must change together, they belong in the same object.
- **Bulk analytics.** Scanning every object means one request per object. Model the
  aggregate separately, or use a real analytics store.

## Implementing features here

**State.** Prefer `ctx.storage` over instance fields — an object can be evicted between
requests, so a field is a cache, not the truth. Read from storage, or repopulate fields in
the constructor.

__STORAGE_NOTE__

**Atomic initialisation.** Async work that must finish before any request is served goes in
`ctx.blockConcurrencyWhile(async () => { ... })` in the constructor; requests queue behind
it. Synchronous setup needs no wrapper — it can run in the constructor body directly.

**Scheduled work.** `await ctx.storage.setAlarm(Date.now() + 60_000)` and add an
`async alarm()` method. There is one alarm per object; setting a new time replaces it.
Re-arm inside `alarm()` for a repeating job, and make the handler idempotent — it can be
retried.

**New Durable Object classes.** Add the class, then declare it: bump `migration_tag` in
`cfdo.json` and run `cfdo upload --new-class OtherClass`. A new class also needs its own
binding in the upload metadata, so check `cfdo upload --dry-run` before sending.

**Web pages and static files.** Do not inline HTML, CSS or client JS into `worker.mjs`.
Put them in a directory (conventionally `public/`), set `"assets": "public"` in
`cfdo.json`, and `cfdo upload` publishes it: `public/index.html` is served at `/`,
`public/app.js` at `/app.js`. Files are matched before the worker runs, so the worker only
sees requests with no matching file — give the API its own prefix (`/api/...`) and fetch it
from the page. Never put anything under `public/__cfdo/`; upload refuses it because it
would shadow the admin routes. `env.ASSETS.fetch(request)` reads a file from worker code.

**Routing.** Derive the id from something stable in the request (a room name, a user id, a
tenant) with `idFromName`. Use `newUniqueId()` only when nothing stable exists — and then
record the id yourself, because nothing else can enumerate it.

## Operating it

```sh
cfdo list              # every DO namespace on the account (needs no project dir)
cfdo status            # deployed? namespace id, object counts, admin health
cfdo status --objects  # every object id with the name it was routed by
cfdo upload            # push worker.mjs and the assets dir, apply any pending migration
cfdo upload --dry-run  # print the exact upload metadata, send nothing
cfdo backup            # -> ./backups/__SCRIPT_NAME__-<timestamp>/
cfdo restore <dir>     # --mode merge (default) or --mode replace
```

Credentials come from the environment, else `~/.cfdo/settings.json` (`cfdo init`).
Run `cfdo status` first when anything looks wrong — it compares local and deployed state.

## Rules that matter here

**Migrations are tag-based.** To change classes, bump `migration_tag` in `cfdo.json` and
upload. Do not hand-edit `.cfdo/state.json` unless it was lost — it records what
Cloudflare already applied, and cfdo derives `old_tag -> new_tag` from it. A code-only
change needs no bump; upload will report `no migration`.

**Never delete the `/__cfdo/` routes from `worker.mjs`.** They are the only way to read
Durable Object storage — no Cloudflare API can do it — so removing them breaks backup and
restore.

**Keep the name registration in the routing code.** The worker records every name it
routes to in a `__cfdo_index` object, and that registry is what makes objects findable —
Cloudflare's listing API can report zero for a namespace that has live objects. `cfdo
backup` unions the two sources, so do not rely on either alone:

- the API lags for sqlite namespaces, and once it catches up it also counts the
  `__cfdo_index` object itself;
- the index is immediate, but only knows objects routed by name through the generated
  code, and never lists itself.

`cfdo list --deep` shows both counts side by side. Switching to `newUniqueId()` means the
app must record ids itself and pass them to `cfdo backup --ids-file`.

**`cfdo restore --mode replace` is destructive.** It wipes each target object's storage
first, and it is not atomic across objects: a failure partway leaves earlier objects
already restored. Prefer `--mode merge`, and confirm with the user before running `replace`
against anything live.

**A 401 from an admin route means the secret differs** from the one bound at upload time.
Fix with `cfdo init --script __SCRIPT_NAME__ --secret ...`, then re-upload.

**Deleting a class destroys its objects' data**, permanently and with no undo.
`cfdo upload --deleted-class X` is not reversible — back up first.

## Verifying a change

```sh
cfdo upload && cfdo status
curl "$(cfdo status --json | python3 -c 'import json,sys; print(json.load(sys.stdin)["worker_url"])')/"
```

`cfdo upload --dry-run` lists every static file it would publish — check it when a page
404s or still shows old content.

A brand-new `workers.dev` hostname returns `tls: handshake failure` for a few minutes
while its certificate is issued. The script, its bindings and the namespace are already
live at that point — only the public hostname lags.

Limits (per-object storage size, request duration, value sizes) have been raised several
times; check Cloudflare's current Durable Objects limits page rather than assuming.
