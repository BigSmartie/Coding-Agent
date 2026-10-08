package session

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BigSmartie/Coding-Agent/internal/message"
	"github.com/BigSmartie/Coding-Agent/internal/safety"
)

func TestSessionPreservesOpaqueContinuationBytes(t *testing.T) {
	for _, fixture := range []struct{ protocol, item, field string }{
		{"openai_responses", `{"type":"reasoning","encrypted_content":"sk-opaqueBytes12345678","summary":[]}`, "encrypted_content"},
		{"anthropic_messages", `{"type":"redacted_thinking","data":"sk-opaqueBytes12345678"}`, "data"},
		{"anthropic_messages", `{"type":"thinking","thinking":"Ordinary reasoning.","signature":"sk-opaqueBytes12345678"}`, "signature"},
	} {
		t.Run(fixture.field, func(t *testing.T) {
			store := Store{Dir: t.TempDir()}
			record := NewRecord(t.TempDir(), []message.Message{{Role: message.RoleProviderState, ProviderState: &message.ProviderState{Protocol: fixture.protocol, Items: []json.RawMessage{json.RawMessage(fixture.item)}}}})
			if err := store.Save(record); err != nil {
				t.Fatal(err)
			}
			loaded, err := store.Load(record.ID)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.ResumeError != "" {
				t.Fatal(loaded.ResumeError)
			}
			var item map[string]any
			if err := json.Unmarshal(loaded.Messages[0].ProviderState.Items[0], &item); err != nil {
				t.Fatal(err)
			}
			if item[fixture.field] != "sk-opaqueBytes12345678" {
				t.Fatalf("opaque bytes were rewritten: %v", item)
			}
		})
	}
}

func TestRedactedSignedReasoningDisablesResumeButKeepsHistory(t *testing.T) {
	store := Store{Dir: t.TempDir(), Context: safety.WithSecrets(context.Background(), "private-live-credential")}
	record := NewRecord(t.TempDir(), []message.Message{
		message.UserMessage("history private-live-credential"),
		{Role: message.RoleProviderState, ProviderState: &message.ProviderState{Protocol: "anthropic_messages", Items: []json.RawMessage{json.RawMessage(`{"type":"thinking","thinking":"private-live-credential","signature":"signed-by-provider"}`)}}},
	})
	if err := store.Save(record); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ResumeError == "" || !strings.Contains(loaded.Messages[0].Content, "[REDACTED]") {
		t.Fatal("signed redaction must retain history and disable replay")
	}
	data, err := os.ReadFile(filepath.Join(store.Dir, record.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "private-live-credential") {
		t.Fatal("credential persisted")
	}
}

func TestOpaqueFieldCannotPersistRuntimeCredential(t *testing.T) {
	store := Store{Dir: t.TempDir(), Context: safety.WithSecrets(context.Background(), "actual-runtime-credential")}
	record := NewRecord(t.TempDir(), []message.Message{{Role: message.RoleProviderState, ProviderState: &message.ProviderState{Protocol: "openai_responses", Items: []json.RawMessage{json.RawMessage(`{"type":"reasoning","encrypted_content":"actual-runtime-credential"}`)}}}})
	if err := store.Save(record); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ResumeError == "" {
		t.Fatal("redacted opaque state was allowed to resume")
	}
	if strings.Contains(string(loaded.Messages[0].ProviderState.Items[0]), "actual-runtime-credential") {
		t.Fatal("opaque state persisted a credential")
	}
}
