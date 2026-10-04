# PLAN — kvd (in-memory etcd-inspired KV store)

## 1. File layout

```
go.mod                                module kvd, go 1.27.1 (pinned, stdlib only)
store/store.go                        Store interface, Entry, sentinel errors, Mem impl (sync.RWMutex)
store/store_test.go                   direct unit tests of Mem
api/api.go                            HTTP handlers + routing + JSON/error mapping
api/api_test.go                       unit tests vs hand-written fake Store (injectable errors), httptest.NewRecorder
cmd/kvd/main.go                       main: wires Mem + api, flag-configurable listen addr
internal/integration/integration_test.go  httptest.NewServer + net/http client, AC-KV1..KV8 end to end
PLAN.md / README.md / ACCEPTANCE_REPORT.md / .gitignore
```

## 2. Implementation steps (small, compile+test each layer before the next)

1. `go.mod` (`module kvd`, `go 1.27.1`).
2. `store`: interface + validation + Mem; run `go test ./store`.
3. `api`: handlers over the `Store` interface; run `go test ./api -cover`.
4. `cmd/kvd`: trivial main.
5. `internal/integration`: real server end-to-end tests.
6. Gates, then ACCEPTANCE_REPORT.md, README, .gitignore, push, PR.

## 3. Design decisions

- Keys: plain `string`; values `[]byte`; revisions `int64` starting at 1.
- `Store` interface: `Put(key, value, ifMatch) (Entry, created bool, err)`, `Get(key) (Entry, err)`,
  `Delete(key, ifMatch) (revision int64, err)`, `List(prefix) ([]Entry, err)`, `Revision() int64`.
  `ifMatch == 0` means "no precondition" (valid If-Match values are positive).
- Sentinel errors shared by store/api/fake: `ErrKeyEmpty`, `ErrKeyNotUTF8`, `ErrKeyTooLarge`,
  `ErrValueTooLarge`, `ErrNotFound`, `ErrRevisionMismatch`; api maps them to 400/413/404/412/500.
- Failed ops never increment the global revision; successful put always increments (even same bytes);
  delete+recreate restarts `version` at 1 with a new `create_revision`.
- Copy-on-input and copy-on-output for value slices; `List` returns cloned entries from one
  RLock-protected snapshot, sorted lexicographically.
- Routing: `http.ServeMux` with `/healthz`, `/v1/keys` (list, GET only), `/v1/keys/` (put/get/delete
  by suffix key). `r.URL.Path` is already URL-decoded once by net/http, so encoded slashes
  (`a%2Fb`) decode exactly once; empty suffix is rejected 400 before touching the store.
- `If-Match`: strict positive decimal (digits only, > 0); malformed → 400. Put to a missing key
  with If-Match → 412; delete of missing key → 404.
- Body read via `io.LimitReader(_, MaxValueBytes+1)`; > 1 MiB → 413 (store also validates).
- Responses: PUT `{key, create_revision, mod_revision, version}` (201 create / 200 update);
  GET adds `value` (base64 std); DELETE `{key, revision}`; list is a JSON array (`[]` when empty);
  errors `{"error": "..."}`; `/healthz` → 200 `ok` text.

## 4. Unit tests

- `store` (direct): put-new (rev/version semantics), put-existing, put same bytes still bumps rev,
  get exact bytes / missing, delete (rev bump, second delete 404, delete-missing-with-IfMatch 404),
  delete+recreate, If-Match put/delete success+failure without mutation, key/value validation
  boundaries (0/256/257-byte keys, non-UTF-8 key, 1 MiB / 1 MiB+1 values), copy-on-input and
  copy-on-output, list prefix + lexicographic order + snapshot isolation, 50x100 concurrent puts
  (revision == 5000) with concurrent readers for `-race`.
- `api` (hand-written fake `Store` with injectable per-method funcs): status/JSON for put 201/200,
  If-Match parsed & forwarded, malformed If-Match → 400 (fake untouched), empty key → 400,
  oversized body → 413 (fake untouched), error mapping 400/404/412/413/500, get base64 round trip,
  encoded-slash key decoding (`/v1/keys/a%2Fb` → key `a/b`), delete 200/404/412, list passthrough
  + prefix forwarding + `[]`, healthz 200 `ok`, wrong method → 405.

## 5. Integration tests → acceptance criteria map

| AC | Integration test (`internal/integration`, httptest.NewServer + http client) |
|---|---|
| AC-KV1 | TestACKV1_PutNew: 201, version=1, create==mod==global rev (next put rev+1) |
| AC-KV2 | TestACKV2_PutExisting: 200, version 2, create_revision unchanged, mod updated |
| AC-KV3 | TestACKV3_Get: binary bytes round-trip; missing → 404 |
| AC-KV4 | TestACKV4_Delete: delete bumps global rev; second delete 404; get 404 |
| AC-KV5 | TestACKV5_List: prefix filter + lexicographic order; empty prefix lists all |
| AC-KV6 | TestACKV6_IfMatch: wrong → 412 without mutation; correct succeeds; missing+IfMatch put → 412; malformed → 400 |
| AC-KV7 | TestACKV7_Validation: empty key 400; 257-byte key 400; >1 MiB 413; 256-byte key and 1 MiB value accepted; non-UTF-8 key 400; encoded-slash key works |
| AC-KV8 | TestACKV8_ConcurrentWriters: 50 goroutines x 100 puts over HTTP; final put observes revision 5001; race detector gate |
| AC-KV9 | gate commands below |

## 6. Concurrency risks and mitigations

- Global revision + map mutation must be atomic → single `sync.RWMutex`; all mutating ops take the
  write lock; get/list take the read lock. Revision increment happens inside the lock after all
  validation/precondition checks, so failures never bump it.
- Returned `Entry` values alias stored slices → clone on every output path (Put/Get/List).
- Caller mutating its value slice after Put → copy input slice before storing.
- List during concurrent writes → snapshot taken under one RLock, then sorted.
- HTTP layer is stateless per request; the shared store is the only shared state.

## 7. Acceptance gates (must all pass before publishing)

1. `gofmt -l .` prints nothing.
2. `go vet ./...` exits 0.
3. `CGO_ENABLED=1 go test -race -count=1 ./...` passes (CGO only for race detector).
4. `go test -cover ./store ./api` shows >= 80% statement coverage for both packages.
5. No third-party imports; `go.mod` has no require/replace directives.

After gates pass: create branch `harness/kvd-ba850d77595b` from `main`, push all deliverables in one
commit via the Gateway multi-file push tool, open a PR against `main` whose body summarizes the
acceptance report. SPEC.md and TASK_PROMPT.md are never pushed.
