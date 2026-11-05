package secrets

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var ctx = context.Background()

func key(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }

func open(t *testing.T, k []byte) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secrets.db")
	s, err := OpenStore(path, k)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func TestPutGetDelete(t *testing.T) {
	s, _ := open(t, key(1))
	if _, found, err := s.Get(ctx, "engine", "openrouter"); err != nil || found {
		t.Fatalf("absent: found=%v err=%v", found, err)
	}
	if err := s.Put(ctx, "engine", "openrouter", "one"); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, "engine", "openrouter", "two"); err != nil {
		t.Fatal(err)
	}
	if v, found, err := s.Get(ctx, "engine", "openrouter"); err != nil || !found || v != "two" {
		t.Fatalf("got %q %v %v", v, found, err)
	}
	if err := s.Delete(ctx, "engine", "openrouter"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "engine", "openrouter"); err != nil {
		t.Fatalf("deleting an absent secret: %v", err)
	}
	if _, found, _ := s.Get(ctx, "engine", "openrouter"); found {
		t.Fatal("still there")
	}
}

func TestNamespacesAreSeparate(t *testing.T) {
	s, _ := open(t, key(1))
	if err := s.Put(ctx, "calld", "openrouter", "x"); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := s.Get(ctx, "engine", "openrouter"); found {
		t.Fatal("engine read calld's secret")
	}
}

func TestMovedRowDoesNotOpen(t *testing.T) {
	s, _ := open(t, key(1))
	if err := s.Put(ctx, "engine", "a", "v"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE secrets SET name = 'b' WHERE name = 'a'`); err != nil {
		t.Fatal(err)
	}
	_, _, err := s.Get(ctx, "engine", "b")
	if !errors.Is(err, ErrDoesNotOpen) {
		t.Fatalf("err = %v", err)
	}
}

func TestWrongKeyDoesNotOpen(t *testing.T) {
	s, path := open(t, key(1))
	if err := s.Put(ctx, "engine", "a", "v"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	other, err := OpenStore(path, key(2))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	_, _, err = other.Get(ctx, "engine", "a")
	if !errors.Is(err, ErrDoesNotOpen) {
		t.Fatalf("err = %v", err)
	}
}

func TestWrongLengthKeyNamesNoBytes(t *testing.T) {
	_, err := OpenStore(filepath.Join(t.TempDir(), "s.db"), []byte("short-secret-bytes"))
	if err == nil || strings.Contains(err.Error(), "short-secret-bytes") {
		t.Fatalf("err = %v", err)
	}
}

func TestConcurrentPutGet(t *testing.T) {
	s, _ := open(t, key(1))
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			name, val := fmt.Sprintf("n%d", i), strings.Repeat(fmt.Sprint(i), 50)
			if err := s.Put(ctx, "engine", name, val); err != nil {
				t.Error(err)
				return
			}
			if v, found, err := s.Get(ctx, "engine", name); err != nil || !found || v != val {
				t.Errorf("%s: %q %v %v", name, v, found, err)
			}
		}()
	}
	wg.Wait()
}
