// Package store implements the kvd in-memory key-value store: a Store
// interface, shared error values, and a thread-safe memory implementation.
package store

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	// MaxKeyBytes is the maximum length of a key in bytes.
	MaxKeyBytes = 256
	// MaxValueBytes is the maximum length of a value in bytes (1 MiB).
	MaxValueBytes = 1 << 20
)

// Sentinel errors shared by the store, the HTTP layer, and test fakes.
var (
	ErrKeyEmpty         = errors.New("store: key must not be empty")
	ErrKeyNotUTF8       = errors.New("store: key must be valid UTF-8")
	ErrKeyTooLarge      = errors.New("store: key exceeds 256 bytes")
	ErrValueTooLarge    = errors.New("store: value exceeds 1 MiB")
	ErrNotFound         = errors.New("store: key not found")
	ErrRevisionMismatch = errors.New("store: mod revision mismatch")
)

// Entry describes one stored key and its revision metadata.
type Entry struct {
	Key            string
	Value          []byte
	CreateRevision int64
	ModRevision    int64
	Version        int64
}

// clone returns a copy of e whose Value slice shares no memory with e's.
func (e Entry) clone() Entry {
	e.Value = append([]byte(nil), e.Value...)
	return e
}

// Store is the key-value contract used by the HTTP layer and test fakes.
// ifMatch == 0 means "no compare-and-swap precondition"; otherwise it must
// equal the key's current ModRevision or the call fails with
// ErrRevisionMismatch and leaves the store unchanged.
type Store interface {
	// Put stores value under key. It reports whether the key was created.
	Put(key string, value []byte, ifMatch int64) (Entry, bool, error)
	// Get returns the entry for key or ErrNotFound.
	Get(key string) (Entry, error)
	// Delete removes key and returns the global revision of the deletion.
	Delete(key string, ifMatch int64) (int64, error)
	// List returns a consistent snapshot of all entries whose key starts
	// with prefix, sorted lexicographically by key.
	List(prefix string) ([]Entry, error)
	// Revision returns the current global revision.
	Revision() int64
}

// Mem is a thread-safe in-memory Store backed by a map and a sync.RWMutex.
type Mem struct {
	mu       sync.RWMutex
	revision int64
	entries  map[string]Entry
}

// NewMem returns an empty in-memory store.
func NewMem() *Mem {
	return &Mem{entries: make(map[string]Entry)}
}

var _ Store = (*Mem)(nil)

func validateKey(key string) error {
	if key == "" {
		return ErrKeyEmpty
	}
	if !utf8.ValidString(key) {
		return ErrKeyNotUTF8
	}
	if len(key) > MaxKeyBytes {
		return ErrKeyTooLarge
	}
	return nil
}

func validateValue(value []byte) error {
	if len(value) > MaxValueBytes {
		return ErrValueTooLarge
	}
	return nil
}

// Put implements Store. A successful put always increments the global
// revision, even when the stored bytes are unchanged.
func (m *Mem) Put(key string, value []byte, ifMatch int64) (Entry, bool, error) {
	if err := validateKey(key); err != nil {
		return Entry{}, false, err
	}
	if err := validateValue(value); err != nil {
		return Entry{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, exists := m.entries[key]
	if ifMatch > 0 && (!exists || cur.ModRevision != ifMatch) {
		return Entry{}, false, ErrRevisionMismatch
	}
	m.revision++
	stored := append([]byte(nil), value...) // copy on input
	if !exists {
		e := Entry{
			Key:            key,
			Value:          stored,
			CreateRevision: m.revision,
			ModRevision:    m.revision,
			Version:        1,
		}
		m.entries[key] = e
		return e.clone(), true, nil
	}
	cur.Value = stored
	cur.ModRevision = m.revision
	cur.Version++
	m.entries[key] = cur
	return cur.clone(), false, nil
}

// Get implements Store.
func (m *Mem) Get(key string) (Entry, error) {
	if err := validateKey(key); err != nil {
		return Entry{}, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.entries[key]
	if !ok {
		return Entry{}, ErrNotFound
	}
	return e.clone(), nil
}

// Delete implements Store. A missing key yields ErrNotFound regardless of
// ifMatch; a wrong ifMatch yields ErrRevisionMismatch without mutation.
func (m *Mem) Delete(key string, ifMatch int64) (int64, error) {
	if err := validateKey(key); err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.entries[key]
	if !ok {
		return 0, ErrNotFound
	}
	if ifMatch > 0 && cur.ModRevision != ifMatch {
		return 0, ErrRevisionMismatch
	}
	delete(m.entries, key)
	m.revision++
	return m.revision, nil
}

// List implements Store.
func (m *Mem) List(prefix string) ([]Entry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Entry, 0, len(m.entries))
	for k, e := range m.entries {
		if strings.HasPrefix(k, prefix) {
			out = append(out, e.clone())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// Revision implements Store.
func (m *Mem) Revision() int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.revision
}
