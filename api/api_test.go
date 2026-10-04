package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kvd/store"
)

// fakeStore is a hand-written Store fake with per-method injectable behavior.
type fakeStore struct {
	putFn    func(key string, value []byte, ifMatch int64) (store.Entry, bool, error)
	getFn    func(key string) (store.Entry, error)
	deleteFn func(key string, ifMatch int64) (int64, error)
	listFn   func(prefix string) ([]store.Entry, error)
	revFn    func() int64
}

var errNotConfigured = errors.New("fake: method not configured")

func (f *fakeStore) Put(key string, value []byte, ifMatch int64) (store.Entry, bool, error) {
	if f.putFn == nil {
		return store.Entry{}, false, errNotConfigured
	}
	return f.putFn(key, value, ifMatch)
}

func (f *fakeStore) Get(key string) (store.Entry, error) {
	if f.getFn == nil {
		return store.Entry{}, errNotConfigured
	}
	return f.getFn(key)
}

func (f *fakeStore) Delete(key string, ifMatch int64) (int64, error) {
	if f.deleteFn == nil {
		return 0, errNotConfigured
	}
	return f.deleteFn(key, ifMatch)
}

func (f *fakeStore) List(prefix string) ([]store.Entry, error) {
	if f.listFn == nil {
		return nil, errNotConfigured
	}
	return f.listFn(prefix)
}

func (f *fakeStore) Revision() int64 {
	if f.revFn == nil {
		return 0
	}
	return f.revFn()
}

var _ store.Store = (*fakeStore)(nil)

func do(t *testing.T, h http.Handler, method, target, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	} else {
		rdr = strings.NewReader("")
	}
	req := httptest.NewRequest(method, target, rdr)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("response body %q is not a JSON object: %v", rec.Body.String(), err)
	}
	return m
}

func TestHealthz(t *testing.T) {
	h := New(&fakeStore{})
	rec := do(t, h, http.MethodGet, "/healthz", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "ok" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "ok")
	}
	rec = do(t, h, http.MethodPost, "/healthz", "", nil)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /healthz status = %d, want 405", rec.Code)
	}
}

func TestPutCreated(t *testing.T) {
	fake := &fakeStore{}
	var gotKey, gotValue string
	var gotIfMatch int64
	fake.putFn = func(key string, value []byte, ifMatch int64) (store.Entry, bool, error) {
		gotKey, gotValue, gotIfMatch = key, string(value), ifMatch
		return store.Entry{Key: key, CreateRevision: 1, ModRevision: 1, Version: 1}, true, nil
	}
	rec := do(t, New(fake), http.MethodPut, "/v1/keys/k", "hello", nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	m := decodeBody(t, rec)
	if m["key"] != "k" || m["create_revision"] != 1.0 || m["mod_revision"] != 1.0 || m["version"] != 1.0 {
		t.Errorf("unexpected body: %v", m)
	}
	if gotKey != "k" || gotValue != "hello" || gotIfMatch != 0 {
		t.Errorf("store got key=%q value=%q ifMatch=%d", gotKey, gotValue, gotIfMatch)
	}
}

func TestPutUpdated(t *testing.T) {
	fake := &fakeStore{
		putFn: func(key string, value []byte, ifMatch int64) (store.Entry, bool, error) {
			return store.Entry{Key: key, CreateRevision: 1, ModRevision: 2, Version: 2}, false, nil
		},
	}
	rec := do(t, New(fake), http.MethodPut, "/v1/keys/k", "v2", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	m := decodeBody(t, rec)
	if m["version"] != 2.0 || m["mod_revision"] != 2.0 || m["create_revision"] != 1.0 {
		t.Errorf("unexpected body: %v", m)
	}
}

func TestPutIfMatchForwarded(t *testing.T) {
	fake := &fakeStore{}
	var gotIfMatch int64 = -1
	fake.putFn = func(key string, value []byte, ifMatch int64) (store.Entry, bool, error) {
		gotIfMatch = ifMatch
		return store.Entry{}, false, store.ErrRevisionMismatch
	}
	rec := do(t, New(fake), http.MethodPut, "/v1/keys/k", "v", map[string]string{"If-Match": "3"})
	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("status = %d, want 412", rec.Code)
	}
	if gotIfMatch != 3 {
		t.Errorf("ifMatch forwarded = %d, want 3", gotIfMatch)
	}
	m := decodeBody(t, rec)
	if _, ok := m["error"]; !ok {
		t.Errorf("error body missing error field: %v", m)
	}
}

func TestPutIfMatchMalformed(t *testing.T) {
	for _, bad := range []string{"abc", "1.5", "-2", "0", "+3", "3 ", "0x10"} {
		called := false
		fake := &fakeStore{
			putFn: func(key string, value []byte, ifMatch int64) (store.Entry, bool, error) {
				called = true
				return store.Entry{}, false, nil
			},
		}
		rec := do(t, New(fake), http.MethodPut, "/v1/keys/k", "v", map[string]string{"If-Match": bad})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("If-Match %q: status = %d, want 400", bad, rec.Code)
		}
		if called {
			t.Errorf("If-Match %q: store was called despite malformed header", bad)
		}
	}
}

func TestPutEmptyKey(t *testing.T) {
	fake := &fakeStore{
		putFn: func(key string, value []byte, ifMatch int64) (store.Entry, bool, error) {
			t.Error("store must not be called for an empty key")
			return store.Entry{}, false, nil
		},
	}
	rec := do(t, New(fake), http.MethodPut, "/v1/keys/", "v", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestPutOversizedValue(t *testing.T) {
	fake := &fakeStore{
		putFn: func(key string, value []byte, ifMatch int64) (store.Entry, bool, error) {
			t.Error("store must not be called for an oversized value")
			return store.Entry{}, false, nil
		},
	}
	req := httptest.NewRequest(http.MethodPut, "/v1/keys/k", strings.NewReader(strings.Repeat("x", store.MaxValueBytes+1)))
	rec := httptest.NewRecorder()
	New(fake).ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

func TestPutStoreErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"key too large", store.ErrKeyTooLarge, http.StatusBadRequest},
		{"key not utf8", store.ErrKeyNotUTF8, http.StatusBadRequest},
		{"value too large", store.ErrValueTooLarge, http.StatusRequestEntityTooLarge},
		{"unknown error", errors.New("boom"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeStore{
				putFn: func(key string, value []byte, ifMatch int64) (store.Entry, bool, error) {
					return store.Entry{}, false, tc.err
				},
			}
			rec := do(t, New(fake), http.MethodPut, "/v1/keys/k", "v", nil)
			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d", rec.Code, tc.want)
			}
			m := decodeBody(t, rec)
			if m["error"] != tc.err.Error() {
				t.Errorf("error = %v, want %q", m["error"], tc.err.Error())
			}
		})
	}
}

func TestGet(t *testing.T) {
	raw := []byte{0x00, 0xff, 'a'}
	fake := &fakeStore{
		getFn: func(key string) (store.Entry, error) {
			if key != "k" {
				t.Errorf("get key = %q, want k", key)
			}
			return store.Entry{Key: key, Value: raw, CreateRevision: 1, ModRevision: 4, Version: 3}, nil
		},
	}
	rec := do(t, New(fake), http.MethodGet, "/v1/keys/k", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	m := decodeBody(t, rec)
	if m["value"] != base64.StdEncoding.EncodeToString(raw) {
		t.Errorf("value = %v, want base64 %q", m["value"], base64.StdEncoding.EncodeToString(raw))
	}
	if m["mod_revision"] != 4.0 || m["version"] != 3.0 {
		t.Errorf("unexpected body: %v", m)
	}
}

func TestGetNotFound(t *testing.T) {
	fake := &fakeStore{
		getFn: func(key string) (store.Entry, error) { return store.Entry{}, store.ErrNotFound },
	}
	rec := do(t, New(fake), http.MethodGet, "/v1/keys/absent", "", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	m := decodeBody(t, rec)
	if m["error"] != store.ErrNotFound.Error() {
		t.Errorf("error = %v, want %q", m["error"], store.ErrNotFound.Error())
	}
}

func TestGetInvalidKey(t *testing.T) {
	fake := &fakeStore{
		getFn: func(key string) (store.Entry, error) { return store.Entry{}, store.ErrKeyNotUTF8 },
	}
	// %ff decodes once to a single 0xff byte, which is not valid UTF-8.
	rec := do(t, New(fake), http.MethodGet, "/v1/keys/%ff", "", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestEncodedSlashKey(t *testing.T) {
	fake := &fakeStore{}
	var gotKey string
	fake.putFn = func(key string, value []byte, ifMatch int64) (store.Entry, bool, error) {
		gotKey = key
		return store.Entry{Key: key, CreateRevision: 1, ModRevision: 1, Version: 1}, true, nil
	}
	rec := do(t, New(fake), http.MethodPut, "/v1/keys/a%2Fb", "v", nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
	if gotKey != "a/b" {
		t.Errorf("decoded key = %q, want %q", gotKey, "a/b")
	}
}

func TestDelete(t *testing.T) {
	fake := &fakeStore{}
	var gotIfMatch int64 = -1
	fake.deleteFn = func(key string, ifMatch int64) (int64, error) {
		gotIfMatch = ifMatch
		if key != "k" {
			t.Errorf("delete key = %q, want k", key)
		}
		return 5, nil
	}
	rec := do(t, New(fake), http.MethodDelete, "/v1/keys/k", "", map[string]string{"If-Match": "2"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotIfMatch != 2 {
		t.Errorf("ifMatch forwarded = %d, want 2", gotIfMatch)
	}
	m := decodeBody(t, rec)
	if m["key"] != "k" || m["revision"] != 5.0 {
		t.Errorf("unexpected body: %v", m)
	}
}

func TestDeleteErrors(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		ifMatch string
		want    int
	}{
		{"missing", store.ErrNotFound, "", http.StatusNotFound},
		{"mismatch", store.ErrRevisionMismatch, "9", http.StatusPreconditionFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeStore{
				deleteFn: func(key string, ifMatch int64) (int64, error) { return 0, tc.err },
			}
			var headers map[string]string
			if tc.ifMatch != "" {
				headers = map[string]string{"If-Match": tc.ifMatch}
			}
			rec := do(t, New(fake), http.MethodDelete, "/v1/keys/k", "", headers)
			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
	// Malformed If-Match never reaches the store.
	fake := &fakeStore{
		deleteFn: func(key string, ifMatch int64) (int64, error) {
			t.Error("store must not be called for malformed If-Match")
			return 0, nil
		},
	}
	rec := do(t, New(fake), http.MethodDelete, "/v1/keys/k", "", map[string]string{"If-Match": "nope"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("malformed If-Match: status = %d, want 400", rec.Code)
	}
}

func TestList(t *testing.T) {
	fake := &fakeStore{}
	var gotPrefix string
	fake.listFn = func(prefix string) ([]store.Entry, error) {
		gotPrefix = prefix
		return []store.Entry{
			{Key: "a", Value: []byte("1"), CreateRevision: 1, ModRevision: 1, Version: 1},
			{Key: "ab", Value: []byte("2"), CreateRevision: 2, ModRevision: 2, Version: 1},
		}, nil
	}
	rec := do(t, New(fake), http.MethodGet, "/v1/keys?prefix=a", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotPrefix != "a" {
		t.Errorf("prefix forwarded = %q, want %q", gotPrefix, "a")
	}
	var arr []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &arr); err != nil {
		t.Fatalf("list body not a JSON array: %v", err)
	}
	if len(arr) != 2 || arr[0]["key"] != "a" || arr[1]["key"] != "ab" {
		t.Errorf("unexpected list: %v", arr)
	}
	if arr[0]["value"] != base64.StdEncoding.EncodeToString([]byte("1")) {
		t.Errorf("value not base64: %v", arr[0]["value"])
	}
}

func TestListEmpty(t *testing.T) {
	fake := &fakeStore{
		listFn: func(prefix string) ([]store.Entry, error) { return []store.Entry{}, nil },
	}
	rec := do(t, New(fake), http.MethodGet, "/v1/keys", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Errorf("empty list body = %q, want []", got)
	}
}

func TestListError(t *testing.T) {
	fake := &fakeStore{
		listFn: func(prefix string) ([]store.Entry, error) { return nil, errors.New("boom") },
	}
	rec := do(t, New(fake), http.MethodGet, "/v1/keys", "", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	h := New(&fakeStore{})
	rec := do(t, h, http.MethodPatch, "/v1/keys/k", "", nil)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("PATCH /v1/keys/k status = %d, want 405", rec.Code)
	}
	rec = do(t, h, http.MethodPost, "/v1/keys", "", nil)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /v1/keys status = %d, want 405", rec.Code)
	}
}
