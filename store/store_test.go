package store

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestPutNewKey(t *testing.T) {
	s := NewMem()
	e, created, err := s.Put("alpha", []byte("one"), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !created {
		t.Fatal("created = false, want true")
	}
	if e.Version != 1 {
		t.Errorf("Version = %d, want 1", e.Version)
	}
	if e.CreateRevision != 1 || e.ModRevision != 1 {
		t.Errorf("create/mod revision = %d/%d, want 1/1", e.CreateRevision, e.ModRevision)
	}
	if got := s.Revision(); got != 1 {
		t.Errorf("Revision() = %d, want 1", got)
	}
}

func TestPutExistingKey(t *testing.T) {
	s := NewMem()
	e1, _, err := s.Put("alpha", []byte("one"), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	e2, created, err := s.Put("alpha", []byte("two"), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if created {
		t.Fatal("created = true, want false for existing key")
	}
	if e2.Version != 2 {
		t.Errorf("Version = %d, want 2", e2.Version)
	}
	if e2.CreateRevision != e1.CreateRevision {
		t.Errorf("CreateRevision changed: %d -> %d", e1.CreateRevision, e2.CreateRevision)
	}
	if e2.ModRevision != 2 {
		t.Errorf("ModRevision = %d, want 2", e2.ModRevision)
	}
	// A successful put bumps the revision even when bytes are unchanged.
	e3, _, err := s.Put("alpha", []byte("two"), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if e3.ModRevision != 3 || e3.Version != 3 {
		t.Errorf("after same-bytes put: mod=%d version=%d, want 3/3", e3.ModRevision, e3.Version)
	}
}

func TestGet(t *testing.T) {
	s := NewMem()
	want := []byte{0x00, 0xff, 'a', 'b'}
	if _, _, err := s.Put("bin", want, 0); err != nil {
		t.Fatalf("Put: %v", err)
	}
	e, err := s.Get("bin")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(e.Value, want) {
		t.Errorf("Get value = %v, want %v", e.Value, want)
	}
	if _, err := s.Get("absent"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get absent err = %v, want ErrNotFound", err)
	}
	if _, err := s.Get(""); !errors.Is(err, ErrKeyEmpty) {
		t.Errorf("Get empty key err = %v, want ErrKeyEmpty", err)
	}
}

func TestDelete(t *testing.T) {
	s := NewMem()
	if _, _, err := s.Put("k", []byte("v"), 0); err != nil {
		t.Fatalf("Put: %v", err)
	}
	rev, err := s.Delete("k", 0)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if rev != 2 || s.Revision() != 2 {
		t.Errorf("delete revision = %d, global = %d, want 2/2", rev, s.Revision())
	}
	if _, err := s.Get("k"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after delete err = %v, want ErrNotFound", err)
	}
	if _, err := s.Delete("k", 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("second Delete err = %v, want ErrNotFound", err)
	}
	// Failed delete must not bump the revision.
	if got := s.Revision(); got != 2 {
		t.Errorf("Revision after failed delete = %d, want 2", got)
	}
	// Deleting a missing key with If-Match is still ErrNotFound (404).
	if _, err := s.Delete("never", 7); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete missing with ifMatch err = %v, want ErrNotFound", err)
	}
}

func TestDeleteThenRecreate(t *testing.T) {
	s := NewMem()
	e1, _, _ := s.Put("k", []byte("v1"), 0)
	if _, err := s.Delete("k", 0); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	e2, created, err := s.Put("k", []byte("v2"), 0)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !created {
		t.Fatal("recreated key reported created=false")
	}
	if e2.Version != 1 {
		t.Errorf("Version = %d, want 1 after recreate", e2.Version)
	}
	if e2.CreateRevision <= e1.CreateRevision {
		t.Errorf("CreateRevision = %d, want > %d", e2.CreateRevision, e1.CreateRevision)
	}
}

func TestIfMatchPut(t *testing.T) {
	s := NewMem()
	e1, _, _ := s.Put("k", []byte("v1"), 0)
	// Wrong revision: fails, no mutation.
	if _, _, err := s.Put("k", []byte("bad"), e1.ModRevision+9); !errors.Is(err, ErrRevisionMismatch) {
		t.Fatalf("Put wrong ifMatch err = %v, want ErrRevisionMismatch", err)
	}
	got, err := s.Get("k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got.Value, []byte("v1")) || got.ModRevision != e1.ModRevision {
		t.Errorf("state mutated by failed CAS: %+v", got)
	}
	if s.Revision() != e1.ModRevision {
		t.Errorf("Revision = %d, want %d (failed CAS must not bump)", s.Revision(), e1.ModRevision)
	}
	// Missing key with If-Match: 412 semantics.
	if _, _, err := s.Put("missing", []byte("v"), 1); !errors.Is(err, ErrRevisionMismatch) {
		t.Errorf("Put missing with ifMatch err = %v, want ErrRevisionMismatch", err)
	}
	// Correct revision succeeds.
	e2, created, err := s.Put("k", []byte("v2"), e1.ModRevision)
	if err != nil {
		t.Fatalf("Put with correct ifMatch: %v", err)
	}
	if created || e2.Version != 2 || e2.ModRevision != e1.ModRevision+1 {
		t.Errorf("CAS put: created=%v version=%d mod=%d", created, e2.Version, e2.ModRevision)
	}
}

func TestIfMatchDelete(t *testing.T) {
	s := NewMem()
	e1, _, _ := s.Put("k", []byte("v"), 0)
	if _, err := s.Delete("k", e1.ModRevision+1); !errors.Is(err, ErrRevisionMismatch) {
		t.Fatalf("Delete wrong ifMatch err = %v, want ErrRevisionMismatch", err)
	}
	if _, err := s.Get("k"); err != nil {
		t.Errorf("key gone after failed CAS delete: %v", err)
	}
	if s.Revision() != e1.ModRevision {
		t.Errorf("Revision = %d, want %d", s.Revision(), e1.ModRevision)
	}
	rev, err := s.Delete("k", e1.ModRevision)
	if err != nil {
		t.Fatalf("Delete with correct ifMatch: %v", err)
	}
	if rev != e1.ModRevision+1 {
		t.Errorf("delete revision = %d, want %d", rev, e1.ModRevision+1)
	}
}

func TestValidation(t *testing.T) {
	nonUTF8 := string([]byte{0xff, 0xfe})
	cases := []struct {
		name  string
		key   string
		value []byte
		want  error
	}{
		{"empty key", "", []byte("v"), ErrKeyEmpty},
		{"non-utf8 key", nonUTF8, []byte("v"), ErrKeyNotUTF8},
		{"key too large", strings.Repeat("k", MaxKeyBytes+1), []byte("v"), ErrKeyTooLarge},
		{"value too large", "ok", make([]byte, MaxValueBytes+1), ErrValueTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewMem()
			if _, _, err := s.Put(tc.key, tc.value, 0); !errors.Is(err, tc.want) {
				t.Errorf("Put err = %v, want %v", err, tc.want)
			}
			if got := s.Revision(); got != 0 {
				t.Errorf("Revision = %d after rejected put, want 0", got)
			}
		})
	}
	// Boundaries that must be accepted.
	s := NewMem()
	if _, _, err := s.Put(strings.Repeat("k", MaxKeyBytes), []byte("v"), 0); err != nil {
		t.Errorf("256-byte key rejected: %v", err)
	}
	if _, _, err := s.Put("big", make([]byte, MaxValueBytes), 0); err != nil {
		t.Errorf("1 MiB value rejected: %v", err)
	}
	if _, _, err := s.Put("héllo-キー", []byte("utf8"), 0); err != nil {
		t.Errorf("multibyte UTF-8 key rejected: %v", err)
	}
}

func TestValueCopies(t *testing.T) {
	s := NewMem()
	// Copy on input: mutating the caller's slice after Put must not leak.
	in := []byte("abc")
	if _, _, err := s.Put("k", in, 0); err != nil {
		t.Fatalf("Put: %v", err)
	}
	in[0] = 'X'
	e, err := s.Get("k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(e.Value) != "abc" {
		t.Errorf("stored value = %q, want %q (input not copied)", e.Value, "abc")
	}
	// Copy on output: mutating a returned slice must not leak.
	e.Value[0] = 'Y'
	again, err := s.Get("k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(again.Value) != "abc" {
		t.Errorf("stored value = %q, want %q (output not copied)", again.Value, "abc")
	}
	// Put's returned entry is also independent of the stored bytes.
	p, _, _ := s.Put("z", []byte("abc"), 0)
	p.Value[0] = 'Z'
	z, _ := s.Get("z")
	if string(z.Value) != "abc" {
		t.Errorf("stored value = %q after mutating Put result", z.Value)
	}
}

func TestList(t *testing.T) {
	s := NewMem()
	for _, k := range []string{"c", "ba", "abc", "a", "b", "ab"} {
		if _, _, err := s.Put(k, []byte("v-"+k), 0); err != nil {
			t.Fatalf("Put %q: %v", k, err)
		}
	}
	all, err := s.List("")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	wantAll := []string{"a", "ab", "abc", "b", "ba", "c"}
	if len(all) != len(wantAll) {
		t.Fatalf("List(\"\") returned %d entries, want %d", len(all), len(wantAll))
	}
	for i, want := range wantAll {
		if all[i].Key != want {
			t.Errorf("all[%d].Key = %q, want %q", i, all[i].Key, want)
		}
		if string(all[i].Value) != "v-"+want {
			t.Errorf("all[%d].Value = %q, want %q", i, all[i].Value, "v-"+want)
		}
	}
	ab, err := s.List("ab")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(ab) != 2 || ab[0].Key != "ab" || ab[1].Key != "abc" {
		t.Errorf("List(\"ab\") = %+v, want keys [ab abc]", ab)
	}
	none, err := s.List("zzz")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("List(\"zzz\") returned %d entries, want 0", len(none))
	}
	// Mutating a listed entry must not affect the store (snapshot copies).
	all[0].Value[0] = 'X'
	e, _ := s.Get("a")
	if string(e.Value) != "v-a" {
		t.Errorf("store mutated through List result: %q", e.Value)
	}
}

func TestConcurrentWriters(t *testing.T) {
	s := NewMem()
	const goroutines = 50
	const perGoroutine = 100
	var wg sync.WaitGroup
	// Concurrent readers exercise the RWMutex read paths under -race.
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				if _, err := s.List("key-"); err != nil {
					t.Errorf("List: %v", err)
					return
				}
			}
		}()
	}
	var writers sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		writers.Add(1)
		go func(g int) {
			defer writers.Done()
			for i := 0; i < perGoroutine; i++ {
				key := fmt.Sprintf("key-%03d-%03d", g, i)
				if _, _, err := s.Put(key, []byte("v"), 0); err != nil {
					t.Errorf("Put %q: %v", key, err)
				}
			}
		}(g)
	}
	writers.Wait()
	wg.Wait()
	const total = goroutines * perGoroutine
	if got := s.Revision(); got != total {
		t.Errorf("Revision = %d, want %d (one per successful write)", got, total)
	}
	entries, err := s.List("")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != total {
		t.Errorf("List returned %d entries, want %d", len(entries), total)
	}
}
