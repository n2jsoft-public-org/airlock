package i18n

import (
	"errors"
	"fmt"
	"testing"
)

var errSentinel = errors.New("sign-in required")

func useErrorCatalogs(t *testing.T) {
	t.Helper()
	useCatalogs(t,
		map[string]string{"open": "could not open %s: %w", "join": "%s, then %s", "plain": "nothing found", "ui": "Failed: %v"},
		map[string]string{"open": "impossible d'ouvrir %s : %w", "join": "%s, puis %s", "plain": "rien trouvé", "ui": "Échec : %v"})
}

func TestErrorIsEnglishInFrench(t *testing.T) {
	useErrorCatalogs(t)
	SetLanguage("fr")
	err := NewError("open", "db", errSentinel)
	if err.Error() != "could not open db: sign-in required" || fmt.Sprint(err) != err.Error() {
		t.Fatalf("Error() = %q, want English", err.Error())
	}
}

func TestErrorUnwrapsLikeErrorf(t *testing.T) {
	useErrorCatalogs(t)
	err := fmt.Errorf("session: %w", NewError("open", "db", errSentinel))
	if !errors.Is(err, errSentinel) {
		t.Fatal("errors.Is lost the sentinel")
	}
	if ie, ok := errors.AsType[*Error](err); !ok || ie.key != "open" {
		t.Fatal("errors.AsType lost the *Error")
	}
}

func TestLocalizeThroughPlainWrappers(t *testing.T) {
	useErrorCatalogs(t)
	SetLanguage("fr")
	err := fmt.Errorf("session 3: %w", NewError("open", "db", NewError("plain")))
	if got := Localize(err); got != "session 3: impossible d'ouvrir db : rien trouvé" {
		t.Fatalf("Localize = %q", got)
	}
	SetLanguage("en")
	if got := Localize(err); got != err.Error() {
		t.Fatalf("English Localize = %q, want Error()", got)
	}
	if Localize(nil) != "" {
		t.Fatal("Localize(nil) must be empty")
	}
}

func TestLocalizeKeepsSentinelsEnglish(t *testing.T) {
	useErrorCatalogs(t)
	SetLanguage("fr")
	if got := Localize(NewError("open", "db", errSentinel)); got != "impossible d'ouvrir db : sign-in required" {
		t.Fatalf("Localize = %q", got)
	}
}

func TestLocalizeJoinedErrors(t *testing.T) {
	useErrorCatalogs(t)
	SetLanguage("fr")
	err := errors.Join(NewError("plain"), NewError("open", "nothing found", NewError("plain")))
	want := "rien trouvé\nimpossible d'ouvrir nothing found : rien trouvé"
	if got := Localize(err); got != want {
		t.Fatalf("Localize = %q, want %q", got, want)
	}
}

func TestTfLocalizesErrorArgs(t *testing.T) {
	useErrorCatalogs(t)
	SetLanguage("fr")
	if got := Format("ui", NewError("plain")); got != "Échec : rien trouvé" {
		t.Fatalf("Format = %q", got)
	}
}

func TestLocalizeEachErrorInItsOwnText(t *testing.T) {
	useErrorCatalogs(t)
	SetLanguage("fr")
	joined := errors.Join(NewError("open", "nothing found", NewError("plain")), NewError("plain"))
	if got, want := Localize(joined), "impossible d'ouvrir nothing found : rien trouvé\nrien trouvé"; got != want {
		t.Errorf("joined: Localize = %q, want %q", got, want)
	}
	prefixed := fmt.Errorf("nothing found while scanning: %w", NewError("plain"))
	if got, want := Localize(prefixed), "nothing found while scanning: rien trouvé"; got != want {
		t.Errorf("prefixed: Localize = %q, want %q", got, want)
	}
	both := fmt.Errorf("%w; %w", NewError("plain"), NewError("open", "db", NewError("plain")))
	if got, want := Localize(both), "rien trouvé; impossible d'ouvrir db : rien trouvé"; got != want {
		t.Errorf("multi-%%w: Localize = %q, want %q", got, want)
	}
}
