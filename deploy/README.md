# Deploying sqlp_serv

`sqlp_serv` (in `cmd/sqlp_serv`) is the web UI at https://sqlp.dskrzypiec.dev. It
runs on the same server as the repository, behind nginx.

| Piece | Where |
|---|---|
| Binary | `/opt/sqlp_serv/sqlp_serv`, listening on `127.0.0.1:9393` |
| Service | systemd unit `sqlp_serv` (`deploy/sqlp_serv.service`), running as a throwaway user |
| Run log | `/var/lib/sqlp_serv/runs.db`, a SQLite database with every run of the page |
| nginx | `/etc/nginx/sites-available/sqlp.dskrzypiec.dev`, from `deploy/nginx/` plus certbot's HTTPS block |
| Certificate | Let's Encrypt via certbot, renewed automatically |
| DNS | A record `sqlp` → the server's public IP, at GoDaddy |

## Redeploy

After changing the code:

```sh
deploy/redeploy.sh               # tests, build, install, restart, health check
deploy/redeploy.sh --skip-tests
```

It builds the checked-out code, including uncommitted changes. The page footer and
`/healthz` show which commit is running. When `deploy/sqlp_serv.service` differs from
the installed unit, it installs that too, because the unit passes flags that only the
new binary knows.

## Operating it

```sh
systemctl status sqlp_serv
journalctl -u sqlp_serv -f      # one line per request; the SQL is never logged
curl http://127.0.0.1:9393/healthz
```

## The run log

Every run of the page is recorded in `/var/lib/sqlp_serv/runs.db`, as material for
finding sqlp's mistakes: valid SQL it rejects, wrong analyses, misleading diagnostics.
The page footer tells visitors that runs are stored with their IP address.

| Table | One row per | Holds |
|---|---|---|
| `requests` | run | time (UTC), client IP, build, dialect, action, options, the SQL, statement and syntax-error counts, the error when the run failed as a whole (a crash is stored with its stack), notes shown, duration |
| `statements` | statement shown | its text and line, the syntax error, why no result was shown, the diagnostics (a JSON array), and the result: the analysis as JSON, or the formatted SQL for the syntax tree |
| `catalogs` | distinct catalog | the catalog's text, keyed by its SHA-256 |

The schema is in `cmd/sqlp_serv/store.go`. Runs are written in the background, so a
request never waits for the disk. If the writer falls behind, a run is dropped and
the journal says so.

The database never grows past 1 GB (`-db-max-mb`). The server checks it at start and
every 5 minutes. Once it is 90% full, the oldest runs are deleted, with their
statements and any catalogs only they used, until it is 80% full. The journal logs
`pruned the run log` with the count. SQLite reuses the freed space, so the file stays
at about 1 GB but grows no further. A prune of a full database takes about a second.
If a burst fills the last 10% between two checks, runs are dropped, with a warning,
until the next check.

The directory belongs to the service's throwaway user, so open the database as
root. `-readonly` keeps the shell from changing anything:

```sh
sudo sqlite3 -readonly /var/lib/sqlp_serv/runs.db
```

Some queries (they work with the machine's `sqlite3` 3.37):

```sql
.mode box
-- Runs per day: how many failed, and how long they took.
SELECT substr(at, 1, 10) AS day, count(*) AS runs,
       sum(error IS NOT NULL) AS failed, sum(syntax_errors > 0) AS with_syntax_errors,
       round(avg(duration_ms), 1) AS avg_ms, round(max(duration_ms), 1) AS max_ms
FROM requests GROUP BY day ORDER BY day DESC LIMIT 14;

-- Statements sqlp rejected: check each against the engine.
SELECT r.id, r.at, r.dialect, s.idx, s.syntax_error, s.text
FROM statements s JOIN requests r ON r.id = s.request_id
WHERE s.syntax_error IS NOT NULL ORDER BY r.at DESC LIMIT 20;

-- Runs that failed as a whole.
SELECT id, at, ip, dialect, action, substr(error, 1, 120) AS error
FROM requests WHERE error IS NOT NULL ORDER BY at DESC LIMIT 20;

-- Analysis diagnostics, most frequent first.
SELECT r.action, d.value AS diagnostic, count(*) AS n
FROM statements s JOIN requests r ON r.id = s.request_id, json_each(s.diagnostics) d
GROUP BY 1, 2 ORDER BY n DESC LIMIT 20;

-- One run in full.
SELECT r.sql, c.text AS catalog, s.idx, s.result
FROM requests r LEFT JOIN catalogs c ON c.sha256 = r.catalog_sha256
LEFT JOIN statements s ON s.request_id = r.id WHERE r.id = 42;
```

To delete old runs sooner, by date: the shell leaves foreign keys off, so turn them
on, or the runs' statements stay behind.

```sh
sudo sqlite3 /var/lib/sqlp_serv/runs.db "PRAGMA foreign_keys = ON;
  DELETE FROM requests WHERE at < '2026-12-01';
  DELETE FROM catalogs WHERE sha256 NOT IN
    (SELECT catalog_sha256 FROM requests WHERE catalog_sha256 IS NOT NULL);
  VACUUM;"
```

## First-time setup

Already done on this server. To set it up again, add the DNS record, then:

```sh
deploy/setup.sh      # unit, nginx site, certificate
deploy/redeploy.sh
```

`setup.sh` installs the nginx site only if it is missing, because certbot edits
the installed copy. So after changing `deploy/nginx/`, copy the change into
`/etc/nginx/sites-available/sqlp.dskrzypiec.dev` by hand, then run
`sudo nginx -t && sudo systemctl reload nginx`. A changed unit is installed by
`redeploy.sh`.

## Limits

The page is public and every run costs CPU, on a machine shared with other
sites. Three layers keep that bounded:

| Layer | Limit |
|---|---|
| `cmd/sqlp_serv/server.go` | 200 KB of SQL, 2 seconds and 2 MB of HTML per request; 8 requests at a time |
| nginx | 2 runs a second and 2 at a time per address |
| systemd | one CPU core (`CPUQuota`), 512 MB of memory |

## Running it locally

```sh
go run -C cmd/sqlp_serv . -addr 127.0.0.1:9393                        # nothing recorded
go run -C cmd/sqlp_serv . -addr 127.0.0.1:9393 -db /tmp/sqlp_runs.db  # with a run log
go test -C cmd/sqlp_serv ./...
```

`cmd/sqlp_serv` is a module of its own (`cmd/sqlp_serv/go.mod`), so that its SQLite
driver, `modernc.org/sqlite` (pure Go, no cgo), stays out of the library's `go.mod`.
`go test ./...` at the root of the repository skips it.

## Third-party files in the page

The page loads only files built into the binary (its CSP allows no other source):

| File | What | Version |
|---|---|---|
| `web/static/htmx.min.js` | htmx | 2.0.4 |
| `web/static/codemirror/` | CodeMirror 5, the SQL editor: `lib/codemirror.js`, `lib/codemirror.css`, `mode/sql/sql.js` and `LICENSE` (MIT) | 5.65.21 |

The CodeMirror files are unchanged from the npm release, so `npm pack codemirror@5.65.21`
and `sha256sum` show they match. `static/app.js` sets the editor up, and the end of
`static/style.css` restyles it.
