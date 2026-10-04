// Package api exposes the kvd Store over an HTTP/JSON API.
package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"kvd/store"
)

// Handler routes HTTP requests to a Store.
type Handler struct {
	st  store.Store
	mux *http.ServeMux
}

// New returns a Handler serving the kvd API backed by st.
func New(st store.Store) *Handler {
	h := &Handler{st: st, mux: http.NewServeMux()}
	h.mux.HandleFunc("/healthz", h.health)
	h.mux.HandleFunc("/v1/keys", h.list)
	h.mux.HandleFunc("/v1/keys/", h.keyOps)
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

type entryJSON struct {
	Key            string `json:"key"`
	Value          string `json:"value"`
	CreateRevision int64  `json:"create_revision"`
	ModRevision    int64  `json:"mod_revision"`
	Version        int64  `json:"version"`
}

type putResponse struct {
	Key            string `json:"key"`
	CreateRevision int64  `json:"create_revision"`
	ModRevision    int64  `json:"mod_revision"`
	Version        int64  `json:"version"`
}

type deleteResponse struct {
	Key      string `json:"key"`
	Revision int64  `json:"revision"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorResponse{Error: msg})
}

// statusFor maps store sentinel errors to HTTP status codes.
func statusFor(err error) int {
	switch {
	case errors.Is(err, store.ErrKeyEmpty),
		errors.Is(err, store.ErrKeyNotUTF8),
		errors.Is(err, store.ErrKeyTooLarge):
		return http.StatusBadRequest
	case errors.Is(err, store.ErrValueTooLarge):
		return http.StatusRequestEntityTooLarge
	case errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, store.ErrRevisionMismatch):
		return http.StatusPreconditionFailed
	default:
		return http.StatusInternalServerError
	}
}

var errMalformedIfMatch = errors.New("If-Match must be a positive decimal revision")

// parseIfMatch reads the optional If-Match header. It returns 0 when the
// header is absent and errMalformedIfMatch when it is not a positive decimal.
func parseIfMatch(r *http.Request) (int64, error) {
	s := r.Header.Get("If-Match")
	if s == "" {
		return 0, nil
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, errMalformedIfMatch
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, errMalformedIfMatch
	}
	return n, nil
}

// requestKey extracts the already URL-decoded key from the request path.
// r.URL.Path is decoded exactly once by net/http, so encoded slashes in the
// key (e.g. a%2Fb) arrive here as a single decoded "/".
func requestKey(r *http.Request) string {
	return strings.TrimPrefix(r.URL.Path, "/v1/keys/")
}

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (h *Handler) keyOps(w http.ResponseWriter, r *http.Request) {
	key := requestKey(r)
	if key == "" {
		writeError(w, http.StatusBadRequest, store.ErrKeyEmpty.Error())
		return
	}
	switch r.Method {
	case http.MethodPut:
		h.put(w, r, key)
	case http.MethodGet:
		h.get(w, key)
	case http.MethodDelete:
		h.del(w, r, key)
	default:
		w.Header().Set("Allow", "PUT, GET, DELETE")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) put(w http.ResponseWriter, r *http.Request, key string) {
	ifMatch, err := parseIfMatch(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, store.MaxValueBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "reading request body failed")
		return
	}
	if len(body) > store.MaxValueBytes {
		writeError(w, http.StatusRequestEntityTooLarge, store.ErrValueTooLarge.Error())
		return
	}
	e, created, err := h.st.Put(key, body, ifMatch)
	if err != nil {
		writeError(w, statusFor(err), err.Error())
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, putResponse{
		Key:            e.Key,
		CreateRevision: e.CreateRevision,
		ModRevision:    e.ModRevision,
		Version:        e.Version,
	})
}

func (h *Handler) get(w http.ResponseWriter, key string) {
	e, err := h.st.Get(key)
	if err != nil {
		writeError(w, statusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, entryJSON{
		Key:            e.Key,
		Value:          base64.StdEncoding.EncodeToString(e.Value),
		CreateRevision: e.CreateRevision,
		ModRevision:    e.ModRevision,
		Version:        e.Version,
	})
}

func (h *Handler) del(w http.ResponseWriter, r *http.Request, key string) {
	ifMatch, err := parseIfMatch(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rev, err := h.st.Delete(key, ifMatch)
	if err != nil {
		writeError(w, statusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, deleteResponse{Key: key, Revision: rev})
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	entries, err := h.st.List(r.URL.Query().Get("prefix"))
	if err != nil {
		writeError(w, statusFor(err), err.Error())
		return
	}
	out := make([]entryJSON, 0, len(entries))
	for _, e := range entries {
		out = append(out, entryJSON{
			Key:            e.Key,
			Value:          base64.StdEncoding.EncodeToString(e.Value),
			CreateRevision: e.CreateRevision,
			ModRevision:    e.ModRevision,
			Version:        e.Version,
		})
	}
	writeJSON(w, http.StatusOK, out)
}
