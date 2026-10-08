package session

import (
	"path/filepath"
	"testing"

	"github.com/BigSmartie/Coding-Agent/internal/message"
)

func TestStoreSaveLoadAndListSessions(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	record := Record{
		ID:  "session-1",
		CWD: "/tmp/project",
		Messages: []message.Message{
			message.SystemMessage("system"),
			message.UserMessage("hello"),
			message.AssistantMessage("world"),
		},
	}

	if err := store.Save(record); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load("session-1")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != "session-1" || loaded.CWD != "/tmp/project" || len(loaded.Messages) != 3 {
		t.Fatalf("unexpected loaded record: %#v", loaded)
	}

	list, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "session-1" || list[0].MessageCount != 3 {
		t.Fatalf("unexpected list: %#v", list)
	}
}

func TestStoreLatestReturnsMostRecentlyUpdated(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	if err := store.Save(Record{ID: "old", Messages: []message.Message{message.UserMessage("old")}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(Record{ID: "new", Messages: []message.Message{message.UserMessage("new")}}); err != nil {
		t.Fatal(err)
	}

	latest, err := store.Latest()
	if err != nil {
		t.Fatal(err)
	}
	if latest.ID != "new" {
		t.Fatalf("expected latest new, got %#v", latest)
	}
}

func TestStoreRejectsPathTraversal(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	if _, err := store.Load(filepath.Join("..", "escape")); err == nil {
		t.Fatal("expected traversal load to fail")
	}
	if err := store.Save(Record{ID: "../escape"}); err == nil {
		t.Fatal("expected traversal save to fail")
	}
}
