// Package integration exercises the real api handler over the real in-memory
// store through an in-process httptest.Server and a net/http client.
// `go test ./internal/integration/...` runs only these tests; `go test ./...`
// runs everything.
package integration

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"kvd/api"
	"kvd/store"
)

type kvResp struct {
	Key            string `json:"key"`
	Value          string `json:"value"`
	CreateRevision int64  `json:"create_revision"`
	ModRevision    int64  `json:"mod_revision"`
	Version        int64  `json:"version"`
	Revision       int64  `json:"revision"`
	Error          string `json:"error"`
}

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(api.New(store.NewMem()))
	t.Cleanup(srv.Close)
	return srv
}

// request issues one HTTP call and returns the status and decoded body.
func request(t *testing.T, c *http.Client, method, url string, body []byte, headers map[string]string) (int, kvResp) {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var kr kvResp
	if err := json.Unmarshal(data, &kr); err != nil {
		t.Fatalf("%s %s: body %q not decodable: %v", method, url, data, err)
	}
	return resp.StatusCode, kr
}

func put(t *testing.T, c *http.Client, srv *httptest.Server, keyPath string, value []byte, ifMatch string) (int, kvResp) {
	t.Helper()
	var headers map[string]string
	if ifMatch != "" {
		headers = map[string]string{"If-Match": ifMatch}
	}
	return request(t, c, http.MethodPut, srv.URL+"/v1/keys/"+keyPath, value, headers)
}

func get(t *testing.T, c *http.Client, srv *httptest.Server, keyPath string) (int, kvResp) {
	t.Helper()
	return request(t, c, http.MethodGet, srv.URL+"/v1/keys/"+keyPath, nil, nil)
}

func del(t *testing.T, c *http.Client, srv *httptest.Server, keyPath string, ifMatch string) (int, kvResp) {
	t.Helper()
	var headers map[string]string
	if ifMatch != "" {
		headers = map[string]string{"If-Match": ifMatch}
	}
	return request(t, c, http.MethodDelete, srv.URL+"/v1/keys/"+keyPath, nil, headers)
}

func list(t *testing.T, c *http.Client, srv *httptest.Server, prefix string) (int, []kvResp) {
	t.Helper()
	resp, err := c.Get(srv.URL + "/v1/keys?prefix=" + prefix)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var arr []kvResp
	if err := json.Unmarshal(data, &arr); err != nil {
		t.Fatalf("list body %q not a JSON array: %v", data, err)
	}
	return resp.StatusCode, arr
}

func TestHealthz(t *testing.T) {
	srv := newServer(t)
	resp, err := srv.Client().Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Errorf("healthz = %d %q, want 200 ok", resp.StatusCode, body)
	}
}

// AC-KV1: PUT new key returns 201, version=1, create==mod==global revision.
func TestACKV1_PutNew(t *testing.T) {
	srv := newServer(t)
	c := srv.Client()
	status, r := put(t, c, srv, "alpha", []byte("one"), "")
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (error %q)", status, r.Error)
	}
	if r.Version != 1 {
		t.Errorf("version = %d, want 1", r.Version)
	}
	if r.CreateRevision != r.ModRevision || r.CreateRevision != 1 {
		t.Errorf("create/mod = %d/%d, want both 1 (global revision)", r.CreateRevision, r.ModRevision)
	}
	// The revision counter is global: the next write on any key gets rev+1.
	status, r2 := put(t, c, srv, "beta", []byte("two"), "")
	if status != http.StatusCreated || r2.ModRevision != 2 {
		t.Errorf("second put: status=%d mod=%d, want 201 and 2", status, r2.ModRevision)
	}
}

// AC-KV2: PUT existing key returns 200, version increments, create_revision
// unchanged, mod_revision updated.
func TestACKV2_PutExisting(t *testing.T) {
	srv := newServer(t)
	c := srv.Client()
	_, r1 := put(t, c, srv, "k", []byte("v1"), "")
	status, r2 := put(t, c, srv, "k", []byte("v2"), "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if r2.Version != 2 {
		t.Errorf("version = %d, want 2", r2.Version)
	}
	if r2.CreateRevision != r1.CreateRevision {
		t.Errorf("create_revision changed: %d -> %d", r1.CreateRevision, r2.CreateRevision)
	}
	if r2.ModRevision != r1.ModRevision+1 {
		t.Errorf("mod_revision = %d, want %d", r2.ModRevision, r1.ModRevision+1)
	}
}

// AC-KV3: GET returns exactly the stored bytes; missing key returns 404.
func TestACKV3_Get(t *testing.T) {
	srv := newServer(t)
	c := srv.Client()
	raw := []byte{0x00, 0x01, 0xff, 'k', 'v', 0x7f}
	if status, r := put(t, c, srv, "bin", raw, ""); status != http.StatusCreated {
		t.Fatalf("put: status=%d error=%q", status, r.Error)
	}
	status, r := get(t, c, srv, "bin")
	if status != http.StatusOK {
		t.Fatalf("get status = %d, want 200", status)
	}
	got, err := base64.StdEncoding.DecodeString(r.Value)
	if err != nil {
		t.Fatalf("value not base64: %v", err)
	}
	if !bytes.Equal(got, raw) {
		t.Errorf("get bytes = %v, want exactly %v", got, raw)
	}
	status, r = get(t, c, srv, "absent")
	if status != http.StatusNotFound {
		t.Errorf("missing get status = %d, want 404", status)
	}
	if r.Error == "" {
		t.Error("404 body should carry a JSON error message")
	}
}

// AC-KV4: DELETE removes the key and bumps the global revision; second DELETE
// returns 404.
func TestACKV4_Delete(t *testing.T) {
	srv := newServer(t)
	c := srv.Client()
	_, r1 := put(t, c, srv, "k", []byte("v"), "")
	status, r := del(t, c, srv, "k", "")
	if status != http.StatusOK {
		t.Fatalf("delete status = %d, want 200", status)
	}
	if r.Revision != r1.ModRevision+1 {
		t.Errorf("delete revision = %d, want %d (global bump)", r.Revision, r1.ModRevision+1)
	}
	if status, _ := get(t, c, srv, "k"); status != http.StatusNotFound {
		t.Errorf("get after delete = %d, want 404", status)
	}
	if status, _ := del(t, c, srv, "k", ""); status != http.StatusNotFound {
		t.Errorf("second delete = %d, want 404", status)
	}
	// The failed delete did not consume a revision.
	_, r2 := put(t, c, srv, "next", []byte("v"), "")
	if r2.ModRevision != r.Revision+1 {
		t.Errorf("next put mod_revision = %d, want %d", r2.ModRevision, r.Revision+1)
	}
}

// AC-KV5: list with prefix returns only matching keys in lexicographic order;
// empty prefix lists all.
func TestACKV5_List(t *testing.T) {
	srv := newServer(t)
	c := srv.Client()
	for _, k := range []string{"c", "ba", "abc", "a", "b", "ab"} {
		if status, r := put(t, c, srv, k, []byte("v-"+k), ""); status != http.StatusCreated {
			t.Fatalf("put %q: status=%d error=%q", k, status, r.Error)
		}
	}
	status, all := list(t, c, srv, "")
	if status != http.StatusOK {
		t.Fatalf("list status = %d, want 200", status)
	}
	wantAll := []string{"a", "ab", "abc", "b", "ba", "c"}
	if len(all) != len(wantAll) {
		t.Fatalf("list all returned %d entries, want %d", len(all), len(wantAll))
	}
	for i, want := range wantAll {
		if all[i].Key != want {
			t.Errorf("all[%d].key = %q, want %q (lexicographic order)", i, all[i].Key, want)
		}
	}
	_, ab := list(t, c, srv, "ab")
	if len(ab) != 2 || ab[0].Key != "ab" || ab[1].Key != "abc" {
		t.Errorf("prefix ab = %+v, want keys [ab abc]", ab)
	}
	_, none := list(t, c, srv, "zzz")
	if len(none) != 0 {
		t.Errorf("prefix zzz returned %d entries, want 0", len(none))
	}
}

// AC-KV6: If-Match with a wrong revision returns 412 and does not mutate
// state; the correct revision succeeds.
func TestACKV6_IfMatch(t *testing.T) {
	srv := newServer(t)
	c := srv.Client()
	_, r1 := put(t, c, srv, "k", []byte("v1"), "")
	wrong := fmt.Sprint(r1.ModRevision + 9)
	status, r := put(t, c, srv, "k", []byte("bad"), wrong)
	if status != http.StatusPreconditionFailed {
		t.Fatalf("wrong If-Match put status = %d, want 412 (error %q)", status, r.Error)
	}
	// No mutation: value, mod_revision and version unchanged.
	_, g := get(t, c, srv, "k")
	if g.Value != base64.StdEncoding.EncodeToString([]byte("v1")) || g.ModRevision != r1.ModRevision || g.Version != 1 {
		t.Errorf("state mutated by failed CAS: %+v", g)
	}
	// Correct revision succeeds.
	right := fmt.Sprint(r1.ModRevision)
	status, r2 := put(t, c, srv, "k", []byte("v2"), right)
	if status != http.StatusOK || r2.Version != 2 {
		t.Errorf("correct If-Match put: status=%d version=%d, want 200/2", status, r2.Version)
	}
	// Put to a missing key with If-Match returns 412.
	status, _ = put(t, c, srv, "missing", []byte("v"), "1")
	if status != http.StatusPreconditionFailed {
		t.Errorf("put missing with If-Match = %d, want 412", status)
	}
	// Delete CAS: wrong revision -> 412 and key survives.
	status, _ = del(t, c, srv, "k", wrong)
	if status != http.StatusPreconditionFailed {
		t.Errorf("delete wrong If-Match = %d, want 412", status)
	}
	if status, _ := get(t, c, srv, "k"); status != http.StatusOK {
		t.Errorf("key gone after failed CAS delete: %d", status)
	}
	status, _ = del(t, c, srv, "k", fmt.Sprint(r2.ModRevision))
	if status != http.StatusOK {
		t.Errorf("delete with correct If-Match = %d, want 200", status)
	}
	// Malformed If-Match returns 400.
	status, _ = put(t, c, srv, "k", []byte("v"), "not-a-number")
	if status != http.StatusBadRequest {
		t.Errorf("malformed If-Match = %d, want 400", status)
	}
}

// AC-KV7: invalid keys and oversized values are rejected with the specified
// status codes; boundary sizes are accepted.
func TestACKV7_Validation(t *testing.T) {
	srv := newServer(t)
	c := srv.Client()
	// Empty key: the bare /v1/keys/ suffix.
	status, r := request(t, c, http.MethodPut, srv.URL+"/v1/keys/", []byte("v"), nil)
	if status != http.StatusBadRequest {
		t.Errorf("empty key put = %d, want 400 (error %q)", status, r.Error)
	}
	if status, _ := request(t, c, http.MethodGet, srv.URL+"/v1/keys/", nil, nil); status != http.StatusBadRequest {
		t.Errorf("empty key get = %d, want 400", status)
	}
	// Key longer than 256 bytes.
	long := strings.Repeat("k", store.MaxKeyBytes+1)
	if status, _ := put(t, c, srv, long, []byte("v"), ""); status != http.StatusBadRequest {
		t.Errorf("257-byte key = %d, want 400", status)
	}
	// Non-UTF-8 key after URL decoding.
	if status, _ := put(t, c, srv, "%ff%fe", []byte("v"), ""); status != http.StatusBadRequest {
		t.Errorf("non-UTF-8 key = %d, want 400", status)
	}
	// Value larger than 1 MiB.
	if status, _ := put(t, c, srv, "big", make([]byte, store.MaxValueBytes+1), ""); status != http.StatusRequestEntityTooLarge {
		t.Errorf("1 MiB+1 value = %d, want 413", status)
	}
	// Boundaries: exactly 256-byte key and exactly 1 MiB value are accepted.
	if status, r := put(t, c, srv, strings.Repeat("k", store.MaxKeyBytes), []byte("v"), ""); status != http.StatusCreated {
		t.Errorf("256-byte key = %d, want 201 (error %q)", status, r.Error)
	}
	if status, r := put(t, c, srv, "max", make([]byte, store.MaxValueBytes), ""); status != http.StatusCreated {
		t.Errorf("1 MiB value = %d, want 201 (error %q)", status, r.Error)
	}
}

// Encoded slashes in keys are decoded exactly once and round-trip.
func TestEncodedSlashKey(t *testing.T) {
	srv := newServer(t)
	c := srv.Client()
	status, r := put(t, c, srv, "a%2Fb", []byte("slash"), "")
	if status != http.StatusCreated {
		t.Fatalf("put encoded slash = %d, want 201 (error %q)", status, r.Error)
	}
	if r.Key != "a/b" {
		t.Errorf("stored key = %q, want %q", r.Key, "a/b")
	}
	status, g := get(t, c, srv, "a%2Fb")
	if status != http.StatusOK {
		t.Fatalf("get encoded slash = %d, want 200", status)
	}
	if g.Key != "a/b" || g.Value != base64.StdEncoding.EncodeToString([]byte("slash")) {
		t.Errorf("get = %+v, want key a/b with value 'slash'", g)
	}
	_, arr := list(t, c, srv, "a/")
	if len(arr) != 1 || arr[0].Key != "a/b" {
		t.Errorf("list prefix a/ = %+v, want single a/b entry", arr)
	}
}

// AC-KV8: 50 goroutines x 100 puts finish without data races and the final
// revision equals the total number of successful writes.
func TestACKV8_ConcurrentWriters(t *testing.T) {
	srv := newServer(t)
	c := srv.Client()
	const goroutines = 50
	const perGoroutine = 100
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				key := fmt.Sprintf("key-%03d-%03d", g, i)
				req, err := http.NewRequest(http.MethodPut, srv.URL+"/v1/keys/"+key, strings.NewReader("v"))
				if err != nil {
					t.Errorf("NewRequest: %v", err)
					return
				}
				resp, err := c.Do(req)
				if err != nil {
					t.Errorf("put %s: %v", key, err)
					return
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if resp.StatusCode != http.StatusCreated {
					t.Errorf("put %s: status %d, want 201", key, resp.StatusCode)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	const total = goroutines * perGoroutine
	// One more put: its revision must be exactly total+1, proving every one of
	// the concurrent writes succeeded and bumped the global revision once.
	status, r := put(t, c, srv, "final", []byte("v"), "")
	if status != http.StatusCreated {
		t.Fatalf("final put status = %d, want 201", status)
	}
	if r.ModRevision != total+1 {
		t.Errorf("final revision = %d, want %d (5000 writes + 1)", r.ModRevision, total+1)
	}
	_, all := list(t, c, srv, "")
	if len(all) != total+1 {
		t.Errorf("list returned %d keys, want %d", len(all), total+1)
	}
}
