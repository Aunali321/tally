# tally

A private watch tracker you host yourself. It keeps what you watched, where you stopped, your ratings and your lists, and replaces Trakt, Simkl and MDBList for people who stream with [Nuvio](https://github.com/NuvioMedia).

- Mirrors your Nuvio account: every play, progress update and library change arrives within a minute.
- Imports history from Trakt, Simkl, IMDb, Stremio and MDBList. Running an import twice adds nothing twice.
- A web app with a calendar of what you watched, per-title history, ratings, lists and search.
- One Go binary and one SQLite file. No external services at runtime.

## Run it

```sh
docker compose up -d
docker compose logs tally
```

While there are no accounts, the log shows a one-time link to create the first one. Open it and sign up with your email and a password of 8 characters or more.

Sign-up only works with an invite, so strangers cannot create accounts on your server. To add someone, open Settings, Account and create an invite link. It works once and expires in 7 days. From the command line: `docker compose exec tally /tally invite you@example.com`.

Put tally behind a reverse proxy with TLS if it is reachable from outside your network. The proxy should also rate-limit `/login`, `/signup`, `/link` and `/v1/login`, since tally does not.

## Connect Nuvio

In tally, open Settings, Connections, sign in with your Nuvio account and pick the profile to mirror. Your password is used once to get a token and is not stored. tally then pulls that profile's history and keeps syncing every minute. You can switch profile later. For a self-hosted Nuvio backend, set its URL under Advanced.

From the command line instead, which lists the profiles and asks which one:

```sh
docker compose exec -it tally /tally nuvio link you@example.com you@nuvio-account.com
```

## Delete your data

Settings, Account has Delete history (plays, progress, ratings, lists and imports; the account stays and Nuvio is disconnected) and Delete account. Both ask for your password and cannot be undone. Apps that already synced remove the deleted history too.

## Import from another tracker

Settings, Imports takes each source in the form it comes in:

| Source  | What to give it                                                         |
|---------|-------------------------------------------------------------------------|
| Trakt   | The .zip from Settings, Data, Export on trakt.tv                          |
| Simkl   | The backup .zip from simkl.com/apps/backup                              |
| IMDb    | The .csv files from exporting your ratings, watchlist or lists           |
| Stremio | Your Stremio email and password, used once                               |
| MDBList | Your API key from MDBList preferences, used once                         |

Imports run on the server, so you can leave the page. Entries without an IMDb or TMDB id are counted and skipped, since tally has no catalog to tell what they are. Letterboxd and TV Time exports carry no such ids, so they cannot be imported.

The same imports work from the command line:

```sh
docker compose run --rm -v "$PWD/export:/export:ro" tally import trakt you@example.com /export/trakt.zip
```

## Link a TV

The TV shows a code. Enter it in Settings, Devices, or open `http://<server>:8080/link` on your phone. Settings also lists every signed-in device and signs them out one by one or all at once.

## Commands

```
tally serve                                     run the server
tally user add <email>                          create an account
tally user passwd <email>                       reset a password and sign out all devices
tally user delete <email>                       delete an account and everything in it
tally invite <email>                            print a single-use sign-up link from this account
tally import trakt|simkl|imdb <email> <path>    import an export (folder, .zip or file)
tally import stremio <email> <stremio-email>    import a Stremio account
tally import mdblist <email>                    import an MDBList account
tally nuvio link [-profile N] [-backend URL] <email> <nuvio-email>
tally nuvio unlink <email>
tally backup <file>                             copy the database, safe while serving
```

Passwords and API keys are asked for without echo, or read from stdin when piped.

| Variable     | Default    | Meaning                                              |
|--------------|------------|------------------------------------------------------|
| `TALLY_DB`   | `tally.db` | Database file. The image uses `/data/tally.db`.      |
| `TALLY_ADDR` | `:8080`    | Listen address.                                      |
| `TZ`         | `UTC`      | Time zone for days on the calendar and in history.   |

## Backups

Everything lives in the database file. Take a consistent copy while tally runs:

```sh
docker compose exec tally /tally backup /data/backup-$(date +%F).db
```

The copy lands in the `tally_data` volume. Restore by stopping tally and putting the copy in place of `/data/tally.db`.

## Apps and the API

Apps talk to tally over a small JSON API: one sync call with a cursor, and batch writes. See [API.md](API.md).

## Development

```sh
go run ./cmd/tally serve
```

Then open the sign-up link it logs. Or create an account directly:

```sh
go run ./cmd/tally user add you@example.com
```

- `cmd/tally`: the command line and server.
- `internal/store`: SQLite, migrations and the sync rules.
- `internal/api`: the JSON API under `/v1`.
- `internal/web`: the web app (Go templates and htmx).
- `internal/nuvio`: the Nuvio mirror.
- `internal/importer`: the imports.
