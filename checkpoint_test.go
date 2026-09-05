package agentkit

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestMemoryCheckpointStoreCopiesAndDeletesValues(t *testing.T) {
	ctx := context.Background()
	store := &MemoryCheckpointStore{}
	input := []byte("checkpoint")
	if err := store.Set(ctx, "run-1", input); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	input[0] = 'X'

	got, existed, err := store.Get(ctx, "run-1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !existed || string(got) != "checkpoint" {
		t.Fatalf("Get() = %q, %v, want checkpoint, true", got, existed)
	}
	got[0] = 'Y'
	gotAgain, _, err := store.Get(ctx, "run-1")
	if err != nil {
		t.Fatalf("second Get() error = %v", err)
	}
	if string(gotAgain) != "checkpoint" {
		t.Fatalf("mutating Get() result changed stored value to %q", gotAgain)
	}

	if err := store.Delete(ctx, "run-1"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, existed, err = store.Get(ctx, "run-1"); err != nil || existed {
		t.Fatalf("Get() after Delete = existed %v, error %v", existed, err)
	}
	if err := store.Delete(ctx, "run-1"); err != nil {
		t.Fatalf("idempotent Delete() error = %v", err)
	}
}

func TestCheckpointStoreValidatesContextAndID(t *testing.T) {
	store := NewMemoryCheckpointStore()
	if err := store.Set(context.TODO(), "run", nil); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if _, _, err := store.Get(context.Background(), " "); err == nil {
		t.Fatal("Get(empty ID) error = nil")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Delete(canceled, "run"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Delete(canceled context) error = %v, want context.Canceled", err)
	}
}

func TestFileCheckpointStoreRoundTripAndSafeFileName(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := NewFileCheckpointStore(dir)
	if err != nil {
		t.Fatalf("NewFileCheckpointStore() error = %v", err)
	}
	id := "../../outside/checkpoint"
	first := []byte{0, 1, 2, 3}
	if err := store.Set(ctx, id, first); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if err := store.Set(ctx, id, []byte("replacement")); err != nil {
		t.Fatalf("replacement Set() error = %v", err)
	}

	got, existed, err := store.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !existed || !bytes.Equal(got, []byte("replacement")) {
		t.Fatalf("Get() = %q, %v, want replacement, true", got, existed)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != sessionStorageKey(id)+".checkpoint" {
		t.Fatalf("checkpoint entries = %#v", entries)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "outside")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsafe path was created: %v", err)
	}

	if err := store.Delete(ctx, id); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, existed, err = store.Get(ctx, id); err != nil || existed {
		t.Fatalf("Get() after Delete = existed %v, error %v", existed, err)
	}
}

func TestFileSessionStoreProvidesPersistentCheckpointStore(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	first, err := NewFileSessionStore(dir)
	if err != nil {
		t.Fatalf("first NewFileSessionStore() error = %v", err)
	}
	if err := first.CheckpointStore().Set(ctx, "run-1", []byte("saved")); err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	second, err := NewFileSessionStore(dir)
	if err != nil {
		t.Fatalf("second NewFileSessionStore() error = %v", err)
	}
	got, existed, err := second.CheckpointStore().Get(ctx, "run-1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !existed || string(got) != "saved" {
		t.Fatalf("Get() = %q, %v, want saved, true", got, existed)
	}
}
