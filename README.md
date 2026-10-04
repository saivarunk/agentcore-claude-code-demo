# kvd

`kvd` is a single-node, in-memory, etcd-inspired key-value store with an HTTP/JSON API.
It uses only the Go standard library.

## Features

- Operations: put, get, delete, list-by-prefix, health.
- Monotonic global revision counter; each key tracks `create_revision`,
  `mod_revision`, and `version`.
- Optional compare-and-swap on put/delete via the `If-Match: <mod_revision>` header.
- Keys: non-empty UTF-8 strings, max 256 bytes. Values: arbitrary bytes, max 1 MiB.
- Thread safe (`sync.RWMutex`); lists are consistent snapshots in lexicographic order.

## Build and run

```
go build ./...
go run ./cmd/kvd -addr :8080
```

## API

| Method | Path | Behavior |
|---|---|---|
| PUT | `/v1/keys/{key}` | body is the value; 201 on create, 200 on update; honors `If-Match` (412 on mismatch) |
| GET | `/v1/keys/{key}` | 200 JSON with base64 value and revisions; 404 if absent |
| DELETE | `/v1/keys/{key}` | 200 with the deletion revision; 404 if absent; honors `If-Match` |
| GET | `/v1/keys?prefix=` | 200 JSON array of matching entries sorted by key; empty prefix lists all |
| GET | `/healthz` | 200 `ok` |

Keys are URL-decoded once; encoded slashes (`/v1/keys/a%2Fb`) are supported. The empty
suffix `/v1/keys/` is a 400.

Successful responses:

- PUT: `{"key","create_revision","mod_revision","version"}`
- GET: `{"key","value"(base64),"create_revision","mod_revision","version"}`
- DELETE: `{"key","revision"}`
- List: `[{"key","value"(base64),"create_revision","mod_revision","version"}, ...]`

Errors are JSON `{"error": "..."}` with status 400 (invalid key, malformed `If-Match`),
413 (value > 1 MiB), 404 (missing key), or 412 (compare-and-swap mismatch).

### Example

```
curl -X PUT --data-binary 'hello' localhost:8080/v1/keys/greeting
# {"key":"greeting","create_revision":1,"mod_revision":1,"version":1}
curl localhost:8080/v1/keys/greeting
# {"key":"greeting","value":"aGVsbG8=","create_revision":1,"mod_revision":1,"version":1}
curl -X DELETE -H 'If-Match: 1' localhost:8080/v1/keys/greeting
# {"key":"greeting","revision":2}
```

## Testing

```
go test -race -count=1 ./...       # unit + integration tests under the race detector
go test -cover ./store ./api       # statement coverage (both >= 80%)
go test ./internal/integration/... # integration tests only
```

Layout: `store` (interface + in-memory implementation), `api` (HTTP handlers),
`cmd/kvd` (main), `internal/integration` (end-to-end tests against
`httptest.NewServer`).
