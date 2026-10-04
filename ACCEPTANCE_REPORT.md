# ACCEPTANCE REPORT — kvd

Date of run: 2026-10-04. Go toolchain: 1.27.1 (pinned in `go.mod`, standard library only).
All gates were executed in the harness workspace; outputs below are the actual outputs.

## Acceptance criteria

| AC | Result | Test mapping | Evidence |
|---|---|---|---|
| AC-KV1 PUT new key → 201, version=1, create==mod==global rev | PASS | `internal/integration.TestACKV1_PutNew`; `store.TestPutNewKey`; `api.TestPutCreated` | race run ok |
| AC-KV2 PUT existing → 200, version increments, create unchanged, mod updated | PASS | `internal/integration.TestACKV2_PutExisting`; `store.TestPutExistingKey`; `api.TestPutUpdated` | race run ok |
| AC-KV3 GET returns exact stored bytes; missing → 404 | PASS | `internal/integration.TestACKV3_Get` (binary value incl. 0x00/0xff); `store.TestGet`; `api.TestGet`, `api.TestGetNotFound` | race run ok |
| AC-KV4 DELETE bumps global revision; second DELETE → 404 | PASS | `internal/integration.TestACKV4_Delete`; `store.TestDelete`; `api.TestDelete`, `api.TestDeleteErrors` | race run ok |
| AC-KV5 List prefix filter, lexicographic order, empty prefix lists all | PASS | `internal/integration.TestACKV5_List`; `store.TestList`; `api.TestList`, `api.TestListEmpty` | race run ok |
| AC-KV6 If-Match wrong → 412 without mutation; correct succeeds; malformed → 400 | PASS | `internal/integration.TestACKV6_IfMatch`; `store.TestIfMatchPut`, `store.TestIfMatchDelete`; `api.TestPutIfMatchForwarded`, `api.TestPutIfMatchMalformed`, `api.TestDeleteErrors` | race run ok |
| AC-KV7 Empty key / key > 256 B → 400; value > 1 MiB → 413 | PASS | `internal/integration.TestACKV7_Validation`; `store.TestValidation`; `api.TestPutEmptyKey`, `api.TestPutOversizedValue`, `api.TestPutStoreErrors` | race run ok |
| AC-KV8 50 goroutines × 100 puts race-free; final revision == total writes | PASS | `store.TestConcurrentWriters` (direct, with concurrent List readers); `internal/integration.TestACKV8_ConcurrentWriters` (over HTTP; final put observes revision 5001) | `go test -race -count=1 ./...` all ok |
| AC-KV9 `go vet ./...` and `gofmt -l .` report nothing | PASS | gate commands below | both printed nothing |

## Executed commands and actual output

```
$ go test -count=1 ./...
ok  	kvd/api	0.005s
?   	kvd/cmd/kvd	[no test files]
ok  	kvd/internal/integration	0.475s
ok  	kvd/store	9.556s

$ gofmt -l .
(no output)

$ go vet ./...
(no output)

$ go test -race -count=1 ./...
ok  	kvd/api	1.032s
?   	kvd/cmd/kvd	[no test files]
ok  	kvd/internal/integration	2.550s
ok  	kvd/store	1.128s

$ go test -cover -count=1 ./store ./api
ok  	kvd/store	0.042s	coverage: 98.5% of statements
ok  	kvd/api	0.006s	coverage: 97.8% of statements

$ go build ./...
(no output)
```

Note: the store race run above reflects the final bounded-reader test; an earlier
spin-loop variant also passed (`ok kvd/store 102.173s`) and was shortened only for
runtime, with all gates re-run afterwards.

## Coverage

- `store`: 98.5% of statements (requirement ≥ 80%).
- `api`: 97.8% of statements (requirement ≥ 80%).

## Test strategy

- `store` unit tests run directly against the in-memory implementation.
- `api` unit tests run against a hand-written fake `Store` (interface-based, with
  per-method injectable behavior and injectable errors) using
  `httptest.NewRecorder`; the fake never reached the store on malformed
  `If-Match`, empty keys, or oversized bodies is asserted.
- `internal/integration` tests run the real `api` + real `store` in-process via
  `httptest.NewServer`, driven by a `net/http` client, covering AC-KV1..KV8 end
  to end. `go test ./...` runs everything; `go test ./internal/integration/...`
  runs only integration.
- UTF-8 validation, encoded-slash keys (`/v1/keys/a%2Fb`), slice copies on input
  and output, and CAS failures without mutation are all covered.

## Limitations

- In-memory only: no persistence; all data is lost on process exit (out of scope per spec).
- `cmd/kvd` has no automated test (trivial wiring); it is covered by `go build ./...`
  and `go vet ./...`. Coverage requirements apply to `store` and `api` only.
- A put body read failure maps to 400; store-level validation errors are mapped by
  sentinel (400/413/404/412, else 500).
- `If-Match` is strict digits-only positive decimal; values with spaces, signs, or
  other notations are rejected with 400.
