package credentials

import (
	"crypto/rand"
	"errors"
	"os"
	"testing"
)

// This opt-in test touches only a new random test record, then removes it. It
// never lists credentials or reads any user/provider credential reference.
func TestNativeCredentialStoreRoundTrip(t *testing.T) {
	if os.Getenv("MY_CODE_CREDENTIAL_INTEGRATION") != "1" {
		t.Skip("set MY_CODE_CREDENTIAL_INTEGRATION=1 to create and remove a synthetic OS credential")
	}
	id := "integration-test/" + rand.Text()
	secret := "synthetic-test-only-" + rand.Text()
	store := SystemStore{}
	t.Cleanup(func() {
		if err := store.Delete(id); err != nil {
			t.Errorf("remove synthetic credential: %v", err)
		}
	})
	if err := store.Set(id, secret); err != nil {
		t.Fatalf("store synthetic credential: %v", err)
	}
	value, err := store.Get(id)
	if err != nil {
		t.Fatalf("read synthetic credential: %v", err)
	}
	if value != secret {
		t.Fatal("synthetic credential did not round-trip")
	}
	if err := store.Delete(id); err != nil {
		t.Fatalf("delete synthetic credential: %v", err)
	}
	if _, err := store.Get(id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted synthetic credential is still present or unreadable: %v", err)
	}
}
