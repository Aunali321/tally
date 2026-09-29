# tally API v1

Self-hosted watch tracker. Replaces Trakt, Simkl and MDBList sync for Nuvio.
Go single binary, SQLite, no external services. Stores what users do plus the titles and posters clients send with it.

## Items

Every endpoint that touches content uses the same `Item`:

```json
{ "type": "movie", "id": "tt0133093" }
{ "type": "show",  "id": "tt0944947" }
{ "type": "show",  "id": "tt0944947", "season": 1, "episode": 2 }
```

- `type`: `movie` or `show`.
- `id`: opaque string. Clients should prefer IMDb ids (`tt...`). Other ids (`tmdb:1399`, `kitsu:1`) are allowed.
- `season`/`episode`: only on show episodes. Both or neither. Season `0` is specials.
- Server key: `movie:tt1`, `show:tt1`, `show:tt1:1:2`.

## Auth

`Authorization: Bearer <token>` on every `/v1` route except login and device.

- `POST /v1/login` `{email, password, name?}` -> `200 {token}` | `401`. `name` labels the session in Settings, default `App`.
- `POST /v1/device` `{name?}` (body optional) -> `200 {device_code, user_code, expires_in, interval}`. User opens `{server}/link` and enters `user_code` (8 chars, shown as `ABCD-EFGH`), or types it in Settings, Devices. Expires in 600s. Interval 5s. `name` labels the session, default `TV`.
- `POST /v1/device/token` `{device_code}` -> `200 {token}` | `428` pending | `410` expired or unknown
- `GET /link` HTML page (`?code=` prefills). Signed-in browsers only need the code; others also sign in.
- `DELETE /v1/token` revokes the current token -> `204`
- `GET /v1/me` -> `{email}`

Accounts are identified by email and have passwords of at least 8 characters. People sign up at `/signup` with a single-use invite, which a signed-in user creates in Settings or the server owner creates with `tally invite`. `tally user add <email>` creates an account directly and `tally user passwd <email>` resets a password and revokes all of that user's tokens.

## Sync

`GET /v1/sync?since=<rev>` -> everything changed after `rev`. `since=0` (or omitted) returns full state without tombstones.

```json
{
  "rev": 42,
  "watched":    [{ "item": Item, "plays": 2, "watched_at": 1727600000000, "deleted": false }],
  "progress":   [{ "item": Item, "position": 1200000, "duration": 3600000, "updated_at": 1727600000000, "deleted": false }],
  "ratings":    [{ "item": Item, "rating": 8, "rated_at": 1727600000000, "deleted": false }],
  "lists":      [{ "slug": "watchlist", "name": "Watchlist", "deleted": false }],
  "list_items": [{ "list": "watchlist", "item": Item, "added_at": 1727600000000, "deleted": false }]
}
```

- Times are unix milliseconds. Positions and durations are milliseconds.
- Each user has one monotonically increasing `rev`. Every write request bumps it once and stamps all rows it touched.
- Deletes are tombstones (`deleted: true`), returned only when `since > 0`.
- Client loop: store `rev`, call `sync?since=rev` on app start, on resume, and after its own writes.

## Writes

All writes take JSON arrays (batch) and return `204`. Invalid input returns `400 {error}` and writes nothing. Server faults return `500 {"error": "internal error"}` and are logged.

- `PUT /v1/progress` `[{item, position, duration}]`. This is the scrobble. If `position/duration >= 0.9`, the server deletes the progress row and marks the item watched. Completions within an hour of another one count as one play. Progress older than the item's last completion is ignored.
- `DELETE /v1/progress` `[Item]`
- `POST /v1/watched` `[{item, watched_at?}]` records a play: increments `plays`, keeps the latest `watched_at` (default now) and clears progress recorded up to that time. Plays can arrive in any order.
- `DELETE /v1/watched` `[Item]`
- `PUT /v1/ratings` `[{item, rating}]` rating 1..10
- `DELETE /v1/ratings` `[Item]`
- `PUT /v1/lists/{slug}` `{name}` creates or renames. Slug: `[a-z0-9-]{1,64}`.
- `DELETE /v1/lists/{slug}` deletes the list and its items.
- `PUT /v1/lists/{slug}/items` `[Item]` adds items. Creates the list (name = slug) if missing.
- `DELETE /v1/lists/{slug}/items` `[Item]`

Conventional slugs: `watchlist`, `dropped` (hidden/dropped shows). The server gives them no special treatment.

## Metadata

No external services. Clients attach what they already know (from their own addons) to any item they write, and the server keeps the latest snapshot per `type:id`:

```json
{ "type": "show", "id": "tt0944947", "season": 1, "episode": 2,
  "meta": { "title": "Game of Thrones", "year": 2011, "poster": "https://..." } }
```

- `meta` is optional on every `Item` in every write. It describes the movie or show (`type:id`), never the episode. Non-empty fields overwrite; empty fields are ignored.
- `GET /v1/sync` adds `"meta": [{ "type", "id", "title", "year", "poster", "deleted": false }]` with the same `rev` rules as other collections.
- `GET /v1/search?q=<text>&type=movie|show` (type optional) searches titles the user has tracked, by title or by exact id (so titles without a name can still be found). Returns up to 50 of:

  ```json
  { "type": "show", "id": "tt0944947", "title": "Game of Thrones", "year": 2011, "poster": "https://...",
    "watched": true, "plays": 6, "episodes": 5, "lists": ["watchlist"], "rating": 8,
    "progress": { "position": 1200000, "duration": 3300000 } }
  ```

  `plays` counts every viewing (of any episode for shows), `episodes` counts watched episodes. `title`, `year`, `poster`, `episodes`, `rating` and `progress` are omitted when unknown or empty.
- Up next and calendars are computed client-side from `watched` and the client's own episode metadata.

## Out of scope

Global catalog search, trending, comments, related, public lists, social, external ratings (IMDb/RT). Anything that needs data the user did not create.
