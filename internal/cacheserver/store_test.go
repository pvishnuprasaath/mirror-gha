package cacheserver

import (
	"testing"
	"time"
)

func TestStore_ReserveCommitFind_ExactMatch(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}

	id, err := store.Reserve("npm-deps-abc123", "v1")
	if err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	if err := store.Commit(id, 42); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	entry, err := store.Find([]string{"npm-deps-abc123"}, "v1")
	if err != nil {
		t.Fatalf("Find() error = %v", err)
	}
	if entry == nil {
		t.Fatal("Find() = nil, want a match")
	}
	if entry.ID != id || entry.Key != "npm-deps-abc123" || entry.Size != 42 {
		t.Errorf("entry = %+v, want ID=%q Key=npm-deps-abc123 Size=42", entry, id)
	}
}

func TestStore_Find_GenuineMissReturnsNilNoError(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}

	entry, err := store.Find([]string{"nothing-stored"}, "v1")
	if err != nil {
		t.Fatalf("Find() error = %v", err)
	}
	if entry != nil {
		t.Errorf("Find() = %+v, want nil for a genuine miss", entry)
	}
}

func TestStore_Find_PrefixMatchOnRestoreKey(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}

	id, err := store.Reserve("npm-deps-abc123", "v1")
	if err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	if err := store.Commit(id, 10); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	// keys[0] ("npm-deps-xyz999") has no exact match and no prefix
	// match; keys[1] ("npm-deps-") has no exact match but does
	// prefix-match the stored "npm-deps-abc123" — the ordered-list
	// semantics, not just a single-key lookup.
	entry, err := store.Find([]string{"npm-deps-xyz999", "npm-deps-"}, "v1")
	if err != nil {
		t.Fatalf("Find() error = %v", err)
	}
	if entry == nil || entry.ID != id {
		t.Errorf("Find() = %+v, want the npm-deps-abc123 entry via prefix match", entry)
	}
}

func TestStore_Find_MostRecentPrefixMatchWins(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}

	idOld, err := store.Reserve("deps-old", "v1")
	if err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	if err := store.Commit(idOld, 1); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	time.Sleep(2 * time.Millisecond) // ensure a distinct, later CreatedAt

	idNew, err := store.Reserve("deps-new", "v1")
	if err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	if err := store.Commit(idNew, 1); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	entry, err := store.Find([]string{"deps-"}, "v1")
	if err != nil {
		t.Fatalf("Find() error = %v", err)
	}
	if entry == nil || entry.ID != idNew {
		t.Errorf("Find() = %+v, want the more-recently-created deps-new entry", entry)
	}
}

func TestStore_Commit_UnreservedIDIsError(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}

	if err := store.Commit("never-reserved", 1); err == nil {
		t.Fatal("Commit() error = nil, want error for an unreserved id")
	}
}

func TestStore_PersistsAcrossOpenStoreCalls(t *testing.T) {
	root := t.TempDir()

	store1, err := OpenStore(root)
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	id, err := store1.Reserve("persisted-key", "v1")
	if err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	if err := store1.Commit(id, 5); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	// A fresh OpenStore against the same root — simulating a second,
	// separate `mirror run` invocation — must still find the entry.
	store2, err := OpenStore(root)
	if err != nil {
		t.Fatalf("second OpenStore() error = %v", err)
	}
	entry, err := store2.Find([]string{"persisted-key"}, "v1")
	if err != nil {
		t.Fatalf("Find() error = %v", err)
	}
	if entry == nil || entry.ID != id {
		t.Errorf("Find() after reopening = %+v, want the persisted entry", entry)
	}
}
