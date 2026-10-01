# guestbook — a website served from a Durable Object

A per-room message board. Each room is one Durable Object with its own SQLite
database, and the object renders the HTML itself — there is no origin server and
no external database.

```
/              -> redirects to /r/lobby
/r/<room>      -> that room's object: GET renders the page, POST adds a message
/__cfdo/*      -> the cfdo admin routes, unchanged from the scaffold
```

## Why a Durable Object

Two visitors posting at the same moment hit the *same* object, and a Durable
Object processes its requests one at a time. So the insert needs no transaction,
no locking and no read-modify-write race. The tradeoff is that all traffic for a
room is serialised through one instance in one location — right for a guestbook,
wrong for something read-heavy and global.

Rooms are created by visiting them: `/r/anything` works immediately.

## Run it

```sh
export CLOUDFLARE_API_TOKEN=... CLOUDFLARE_ACCOUNT_ID=... CFDO_SECRET=...
# set account_id in cfdo.json, then
cfdo upload
cfdo status
cfdo backup            # captures the messages table: schema + rows
```

## What it demonstrates

- **HTML from a DO**, including a POST/redirect/GET form cycle so a refresh does
  not repost.
- **SQLite storage** — `CREATE TABLE IF NOT EXISTS` in the constructor, then
  plain `sql.exec` with bound parameters.
- **Backup of SQL data.** Verified live: five messages backed up at three, two
  more posted, then `cfdo restore --mode replace` returned the room to exactly
  the three original rows while a sibling room was untouched.
- **Output escaping.** Message bodies go through `esc()`; a posted
  `<script>alert(1)</script>` renders as text.

## Not production ready

Posting is unauthenticated and unthrottled — anyone with the URL can write to
any room. Add auth (or Turnstile) and a rate limit before this holds anything
real.
