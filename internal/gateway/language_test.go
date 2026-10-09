package gateway

import (
	"fmt"
	"strings"
	"testing"

	"github.com/n2jsoft/airlock/internal/i18n"
)

// TestGatewayFailureStaysEnglishInFrench: agents read English whatever the user's language.
func TestGatewayFailureStaysEnglishInFrench(t *testing.T) {
	i18n.SetLanguage("fr")
	t.Cleanup(func() { i18n.SetLanguage("en") })
	deep := fmt.Errorf("session 2: %w", i18n.NewError("cli.connection_test.no_password_given"))
	got := openFailure(deep, &target{name: "orders-db"})
	if got != "could not open orders-db: session 2: no password given" || strings.Contains(got, "aucun") {
		t.Fatalf("openFailure = %q, want English", got)
	}
}
