package gateway

import (
	"context"
	"errors"
	"testing"

	"github.com/Jemonee/simple-openai-gateway/internal/config"
)

func TestAccessTokenConsoleSessionLifecycle(t *testing.T) {
	store := newTestStore(t)
	management := NewManagementService(store)
	auth := NewAdminAuthService(store, &config.ApplicationConfigManager{})
	ctx := context.Background()
	input := ClientTokenInput{
		Name: "console token", Enabled: true, AllowAllModels: true,
		RPM: 60, MaxConcurrency: 4,
	}
	issued, err := management.CreateToken(ctx, input)
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := auth.LoginWithAccessToken("sk-invalid"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("invalid token login error = %v", err)
	}
	rawSession, token, err := auth.LoginWithAccessToken(issued.Secret)
	if err != nil {
		t.Fatal(err)
	}
	if rawSession == issued.Secret {
		t.Fatal("console session reused the access-token plaintext")
	}
	var session AdminSession
	if err := store.db.First(&session).Error; err != nil {
		t.Fatal(err)
	}
	if session.TokenHash != hashSecret(rawSession) || session.TokenHash == hashSecret(issued.Secret) {
		t.Fatalf("unexpected stored session hash %q", session.TokenHash)
	}
	if session.LoginType != ConsoleLoginAccessToken || session.ClientTokenID != token.ID || session.UserID != 0 {
		t.Fatalf("unexpected access-token session: %+v", session)
	}
	principal, err := auth.AuthenticateConsole(rawSession)
	if err != nil || principal.AccessToken == nil || principal.AccessToken.ID != token.ID || principal.IsAdmin() {
		t.Fatalf("AuthenticateConsole() principal = %+v, error = %v", principal, err)
	}
	if _, err := auth.Authenticate(rawSession); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("Authenticate() accepted access-token session: %v", err)
	}

	disabled := input
	disabled.Enabled = false
	if _, err := management.UpdateToken(ctx, token.ID, disabled); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.AuthenticateConsole(rawSession); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("disabled token session error = %v", err)
	}
	if _, _, err := auth.LoginWithAccessToken(issued.Secret); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("disabled token login error = %v", err)
	}

	if _, err := management.UpdateToken(ctx, token.ID, input); err != nil {
		t.Fatal(err)
	}
	rawSession, _, err = auth.LoginWithAccessToken(issued.Secret)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := management.RotateToken(ctx, token.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.AuthenticateConsole(rawSession); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("rotated token session error = %v", err)
	}
	if _, _, err := auth.LoginWithAccessToken(issued.Secret); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("old rotated token login error = %v", err)
	}

	rawSession, _, err = auth.LoginWithAccessToken(rotated.Secret)
	if err != nil {
		t.Fatal(err)
	}
	if err := management.DeleteToken(ctx, token.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.AuthenticateConsole(rawSession); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("deleted token session error = %v", err)
	}
}

func TestManagementPersistsAndRotatesTokenChannelScope(t *testing.T) {
	store := newTestStore(t)
	management := NewManagementService(store)
	ctx := context.Background()
	channels := []Channel{
		{Name: "channel-one", BaseURL: "http://one.invalid/v1", APIKeyCipher: "cipher", Enabled: true},
		{Name: "channel-two", BaseURL: "http://two.invalid/v1", APIKeyCipher: "cipher", Enabled: true},
	}
	if err := store.db.Create(&channels).Error; err != nil {
		t.Fatal(err)
	}
	allowAllChannels := false
	input := ClientTokenInput{
		Name: "channel scoped", Enabled: true, AllowAllModels: true, AllowAllChannels: &allowAllChannels,
		RPM: 60, MaxConcurrency: 4, ChannelIDs: []uint64{channels[1].ID},
	}
	issued, err := management.CreateToken(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if issued.Token.AllowAllChannels || len(issued.Token.ChannelIDs) != 1 || issued.Token.ChannelIDs[0] != channels[1].ID {
		t.Fatalf("issued channel scope = %+v", issued.Token)
	}

	legacyUpdate := ClientTokenInput{Name: input.Name, Enabled: true, AllowAllModels: true, RPM: 120, MaxConcurrency: 4}
	updated, err := management.UpdateToken(ctx, issued.Token.ID, legacyUpdate)
	if err != nil {
		t.Fatal(err)
	}
	if updated.AllowAllChannels || len(updated.ChannelIDs) != 1 || updated.ChannelIDs[0] != channels[1].ID {
		t.Fatalf("legacy update changed channel scope = %+v", updated)
	}

	rotated, err := management.RotateToken(ctx, issued.Token.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Token.AllowAllChannels || len(rotated.Token.ChannelIDs) != 1 || rotated.Token.ChannelIDs[0] != channels[1].ID {
		t.Fatalf("rotated channel scope = %+v", rotated.Token)
	}

	defaultToken, err := management.CreateToken(ctx, ClientTokenInput{Name: "default channels", Enabled: true, AllowAllModels: true, RPM: 60, MaxConcurrency: 4})
	if err != nil {
		t.Fatal(err)
	}
	if !defaultToken.Token.AllowAllChannels || len(defaultToken.Token.ChannelIDs) != 0 {
		t.Fatalf("default channel scope = %+v", defaultToken.Token)
	}
}
