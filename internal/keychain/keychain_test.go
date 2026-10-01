package keychain

import (
	"errors"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestOSStoreRoundTrip(t *testing.T) {
	keyring.MockInit()
	store := NewOSStore()

	if _, err := store.Get("111/eu-west-3/rds/archive/app"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get of a missing entry = %v, want ErrNotFound", err)
	}
	if err := store.Set("111/eu-west-3/rds/archive/app", "s3cret"); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get("111/eu-west-3/rds/archive/app")
	if err != nil || got != "s3cret" {
		t.Fatalf("Get = %q, %v", got, err)
	}
	if err := store.Delete("111/eu-west-3/rds/archive/app"); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete("111/eu-west-3/rds/archive/app"); err != nil {
		t.Fatalf("deleting a missing entry = %v, want no error", err)
	}
}

func TestAvailable(t *testing.T) {
	keyring.MockInit()
	if !Available(NewOSStore()) {
		t.Fatal("a working keychain must be available")
	}
	keyring.MockInitWithError(errors.New("dbus: no session bus"))
	if Available(NewOSStore()) {
		t.Fatal("an unreachable keychain must not be available")
	}
	if Available(nil) {
		t.Fatal("no store is not available")
	}
}

func TestDeleteAll(t *testing.T) {
	keyring.MockInit()
	stores := map[string]Store{"os": NewOSStore(), "memory": NewMemoryStore()}
	for name, store := range stores {
		t.Run(name, func(t *testing.T) {
			for _, account := range []string{"111/eu-west-3/rds/archive/app", "222/eu-west-1/rds/billing/ro"} {
				if err := store.Set(account, "s3cret"); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.DeleteAll(); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Get("222/eu-west-1/rds/billing/ro"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("Get after DeleteAll = %v, want ErrNotFound", err)
			}
			if err := store.DeleteAll(); err != nil {
				t.Fatalf("DeleteAll of an empty keychain = %v, want no error", err)
			}
		})
	}
}

func TestMemoryStore(t *testing.T) {
	store := NewMemoryStore()
	if _, err := store.Get("a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get = %v, want ErrNotFound", err)
	}
	_ = store.Set("a", "1")                   //nolint:errcheck // error ignored in test setup
	if got, _ := store.Get("a"); got != "1" { //nolint:errcheck // error ignored in test assertion
		t.Fatalf("Get = %q", got)
	}
	store.Err = errors.New("locked")
	if err := store.Set("a", "2"); err == nil {
		t.Fatal("Err must fail every call")
	}
}
