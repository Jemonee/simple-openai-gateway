package gateway

import (
	"errors"
	"testing"

	"github.com/Jemonee/simple-openai-gateway/internal/config"

	"gorm.io/gorm"
)

func TestAdminCreatesAndResetsRegularUserCredentials(t *testing.T) {
	store := newTestStore(t)
	auth := NewAdminAuthService(store, &config.ApplicationConfigManager{})

	if _, err := auth.CreateUser("short-password", "too-short", ConsoleRoleUser); err == nil {
		t.Fatal("CreateUser() accepted a short password")
	}
	if _, err := auth.CreateUser("unsupported-role", "initial-password-123", "operator"); err == nil {
		t.Fatal("CreateUser() accepted an unsupported role")
	}

	created, err := auth.CreateUser("regular-user", "initial-password-123", "")
	if err != nil {
		t.Fatal(err)
	}
	if created.Role != ConsoleRoleUser || !created.Enabled {
		t.Fatalf("created user = %+v", created)
	}

	session, user, err := auth.Login(created.Username, "initial-password-123")
	if err != nil {
		t.Fatal(err)
	}
	if user.ID != created.ID || user.Role != ConsoleRoleUser {
		t.Fatalf("logged-in user = %+v", user)
	}
	principal, err := auth.AuthenticateConsole(session)
	if err != nil || !principal.IsUser() || principal.IsAdmin() {
		t.Fatalf("regular-user principal = %+v, error = %v", principal, err)
	}

	if err := auth.ResetPassword(created.ID, "replacement-password-456"); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.AuthenticateConsole(session); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("reset session error = %v", err)
	}
	if _, _, err := auth.Login(created.Username, "initial-password-123"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("old password login error = %v", err)
	}
	if _, resetUser, err := auth.Login(created.Username, "replacement-password-456"); err != nil || resetUser.ID != created.ID {
		t.Fatalf("replacement password login user = %+v, error = %v", resetUser, err)
	}
	if err := auth.ResetPassword(created.ID+1000, "replacement-password-789"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("missing user reset error = %v", err)
	}
}
