package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStateSurvivesRestartAndRejectsOlderGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "player.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := store.AcceptGeneration(2, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil || !accepted {
		t.Fatalf("accept generation: accepted=%v err=%v", accepted, err)
	}
	if err := store.RecordTransition(2, "ACTIVATING", "commit candidate", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.SetEventProgress("epoch-one", 7, 4); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	attempt, err := store.Attempt(2)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.State != "ACTIVATING" || attempt.Intent != "commit candidate" {
		t.Fatalf("unexpected attempt after restart: %#v", attempt)
	}
	accepted, err = store.AcceptGeneration(1, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if err != nil || accepted {
		t.Fatalf("older generation accepted: accepted=%v err=%v", accepted, err)
	}
	epoch, report, watermark, err := store.EventProgress()
	if err != nil {
		t.Fatal(err)
	}
	if epoch != "epoch-one" || report != 7 || watermark != 4 {
		t.Fatalf("unexpected event progress: %q %d %d", epoch, report, watermark)
	}
}

func TestEventProgressCannotMoveBackwards(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "player.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SetEventProgress("epoch", 5, 3); err != nil {
		t.Fatal(err)
	}
	if err := store.SetEventProgress("epoch", 4, 3); err == nil {
		t.Fatal("report sequence moved backwards")
	}
	if err := store.SetEventProgress("epoch", 5, 2); err == nil {
		t.Fatal("event watermark moved backwards")
	}
}

func TestCorruptStoreFailsWithoutReplacingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "player.db")
	corrupt := []byte("not a bbolt database")
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("corrupt state opened")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(corrupt) {
		t.Fatal("corrupt state was silently replaced")
	}
}
