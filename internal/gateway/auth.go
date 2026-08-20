package gateway

import (
	"errors"
	"strings"
	"time"

	"github.com/Jemonee/simple-openai-gateway/internal/config"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

var (
	ErrInvalidCredentials = errors.New("invalid administrator credentials")
	ErrInvalidSession     = errors.New("invalid or expired administrator session")
)

type AdminUserView struct {
	ID        uint64    `json:"id"`
	Username  string    `json:"username"`
	Role      string    `json:"role"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"createdAt"`
}

const (
	ConsoleLoginAdmin       = "admin"
	ConsoleLoginAccessToken = "access_token"
	ConsoleRoleAdmin        = "admin"
	ConsoleRoleUser         = "user"
)

// ConsolePrincipal is the authenticated identity for a management-console session.
// Exactly one of Admin or AccessToken is set according to LoginType.
type ConsolePrincipal struct {
	LoginType   string
	Admin       *AdminUser
	AccessToken *ClientToken
}

func (p *ConsolePrincipal) IsAdmin() bool {
	return p != nil && p.LoginType == ConsoleLoginAdmin && p.Admin != nil && (p.Admin.Role == "" || p.Admin.Role == ConsoleRoleAdmin)
}

func (p *ConsolePrincipal) UserID() uint64 {
	if p == nil {
		return 0
	}
	if p.Admin != nil {
		return p.Admin.ID
	}
	if p.AccessToken != nil {
		return p.AccessToken.OwnerUserID
	}
	return 0
}

func (p *ConsolePrincipal) IsUser() bool {
	return p != nil && p.Admin != nil && p.LoginType == ConsoleLoginAdmin && !p.IsAdmin()
}

func (p *ConsolePrincipal) TokenID() uint64 {
	if p == nil || p.AccessToken == nil {
		return 0
	}
	return p.AccessToken.ID
}

type AdminAuthService struct {
	store         *Store
	configManager *config.ApplicationConfigManager
}

func NewAdminAuthService(store *Store, configManager *config.ApplicationConfigManager) *AdminAuthService {
	return &AdminAuthService{store: store, configManager: configManager}
}

func (s *AdminAuthService) Login(username string, password string) (string, *AdminUser, error) {
	var user AdminUser
	err := s.store.db.Where("username = ? AND enabled = ?", strings.TrimSpace(username), true).First(&user).Error
	if err != nil || bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) != nil {
		return "", nil, ErrInvalidCredentials
	}
	rawToken, err := s.createSession(ConsoleLoginAdmin, user.ID, 0)
	if err != nil {
		return "", nil, err
	}
	return rawToken, &user, nil
}

// LoginWithAccessToken validates a client access token without persisting its plaintext.
func (s *AdminAuthService) LoginWithAccessToken(accessToken string) (string, *ClientToken, error) {
	accessToken = strings.TrimSpace(accessToken)
	if !strings.HasPrefix(accessToken, "sk-") {
		return "", nil, ErrInvalidCredentials
	}
	var token ClientToken
	if err := s.store.db.Where("key_hash = ? AND enabled = ?", hashSecret(accessToken), true).First(&token).Error; err != nil {
		return "", nil, ErrInvalidCredentials
	}
	rawToken, err := s.createSession(ConsoleLoginAccessToken, 0, token.ID)
	if err != nil {
		return "", nil, err
	}
	return rawToken, &token, nil
}

func (s *AdminAuthService) createSession(loginType string, userID uint64, clientTokenID uint64) (string, error) {
	rawToken, err := generateSecret("")
	if err != nil {
		return "", err
	}
	now := time.Now()
	session := AdminSession{
		TokenHash:     hashSecret(rawToken),
		LoginType:     loginType,
		UserID:        userID,
		ClientTokenID: clientTokenID,
		CreatedAt:     now,
		LastSeen:      now,
	}
	if ttl := s.sessionTTL(); ttl > 0 {
		session.ExpiresAt = now.Add(ttl)
	}
	if err := s.store.db.Create(&session).Error; err != nil {
		return "", err
	}
	return rawToken, nil
}

func (s *AdminAuthService) Authenticate(rawToken string) (*AdminUser, error) {
	principal, err := s.AuthenticateConsole(rawToken)
	if err != nil || !principal.IsAdmin() {
		return nil, ErrInvalidSession
	}
	return principal.Admin, nil
}

// AuthenticateConsole resolves either a management administrator or an enabled
// access-token subject. The token record lookup makes revocation effective immediately.
func (s *AdminAuthService) AuthenticateConsole(rawToken string) (*ConsolePrincipal, error) {
	if strings.TrimSpace(rawToken) == "" {
		return nil, ErrInvalidSession
	}
	var session AdminSession
	err := s.store.db.Where("token_hash = ?", hashSecret(rawToken)).First(&session).Error
	if err != nil {
		return nil, ErrInvalidSession
	}
	now := time.Now()
	if ttl := s.sessionTTL(); ttl > 0 {
		expiresAt := session.ExpiresAt
		if !session.CreatedAt.IsZero() {
			expiresAt = session.CreatedAt.Add(ttl)
		}
		if expiresAt.IsZero() || !now.Before(expiresAt) {
			return nil, ErrInvalidSession
		}
	}
	principal := &ConsolePrincipal{LoginType: session.LoginType}
	switch session.LoginType {
	case "", ConsoleLoginAdmin:
		var user AdminUser
		if err := s.store.db.Where("id = ? AND enabled = ?", session.UserID, true).First(&user).Error; err != nil {
			return nil, ErrInvalidSession
		}
		principal.LoginType = ConsoleLoginAdmin
		principal.Admin = &user
	case ConsoleLoginAccessToken:
		var token ClientToken
		if session.ClientTokenID == 0 || s.store.db.Where("id = ? AND enabled = ?", session.ClientTokenID, true).First(&token).Error != nil {
			return nil, ErrInvalidSession
		}
		principal.AccessToken = &token
	default:
		return nil, ErrInvalidSession
	}
	_ = s.store.db.Model(&session).Update("last_seen", time.Now()).Error
	return principal, nil
}

func (s *AdminAuthService) sessionTTL() time.Duration {
	if s.configManager == nil {
		return 0
	}
	hours := config.EffectiveSessionTTLHours(s.configManager.GetConfig())
	if hours <= 0 {
		return 0
	}
	return time.Duration(hours) * time.Hour
}

func (s *AdminAuthService) Logout(rawToken string) error {
	if strings.TrimSpace(rawToken) == "" {
		return nil
	}
	return s.store.db.Where("token_hash = ?", hashSecret(rawToken)).Delete(&AdminSession{}).Error
}

func revokeClientTokenConsoleSessions(db *gorm.DB, tokenID uint64) error {
	if tokenID == 0 {
		return nil
	}
	return db.Where("login_type = ? AND client_token_id = ?", ConsoleLoginAccessToken, tokenID).Delete(&AdminSession{}).Error
}

func (s *AdminAuthService) ChangePassword(userID uint64, currentPassword string, nextPassword string) error {
	if len(nextPassword) < 12 {
		return errors.New("new password must contain at least 12 characters")
	}
	var user AdminUser
	if err := s.store.db.First(&user, userID).Error; err != nil {
		return err
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(currentPassword)) != nil {
		return ErrInvalidCredentials
	}
	nextHash, err := bcrypt.GenerateFromPassword([]byte(nextPassword), 12)
	if err != nil {
		return err
	}
	return s.store.db.Transaction(func(db *gorm.DB) error {
		if err := db.Model(&user).Update("password_hash", string(nextHash)).Error; err != nil {
			return err
		}
		return db.Where("user_id = ?", user.ID).Delete(&AdminSession{}).Error
	})
}

func validateAccountPassword(password string) error {
	if len(password) < 12 {
		return errors.New("password must contain at least 12 characters")
	}
	return nil
}

func normalizeAccountRole(role string) (string, error) {
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "" {
		role = ConsoleRoleUser
	}
	if role != ConsoleRoleAdmin && role != ConsoleRoleUser {
		return "", errors.New("unsupported account role")
	}
	return role, nil
}

func (s *AdminAuthService) ListUsers() ([]AdminUserView, error) {
	var users []AdminUser
	if err := s.store.db.Order("id asc").Find(&users).Error; err != nil {
		return nil, err
	}
	views := make([]AdminUserView, 0, len(users))
	for _, user := range users {
		role := user.Role
		if role == "" {
			role = ConsoleRoleAdmin
		}
		views = append(views, AdminUserView{ID: user.ID, Username: user.Username, Role: role, Enabled: user.Enabled, CreatedAt: user.CreatedAt})
	}
	return views, nil
}

func (s *AdminAuthService) CreateUser(username, password, role string) (*AdminUserView, error) {
	username = strings.TrimSpace(username)
	if username == "" || len([]rune(username)) > 80 {
		return nil, errors.New("username is required and must be at most 80 characters")
	}
	if err := validateAccountPassword(password); err != nil {
		return nil, err
	}
	role, err := normalizeAccountRole(role)
	if err != nil {
		return nil, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return nil, err
	}
	user := AdminUser{Username: username, PasswordHash: string(hash), Role: role, Enabled: true}
	if err := s.store.db.Create(&user).Error; err != nil {
		return nil, err
	}
	return &AdminUserView{ID: user.ID, Username: user.Username, Role: user.Role, Enabled: user.Enabled, CreatedAt: user.CreatedAt}, nil
}

func (s *AdminAuthService) ResetPassword(userID uint64, nextPassword string) error {
	if userID == 0 {
		return gorm.ErrRecordNotFound
	}
	if err := validateAccountPassword(nextPassword); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(nextPassword), 12)
	if err != nil {
		return err
	}
	return s.store.db.Transaction(func(db *gorm.DB) error {
		result := db.Model(&AdminUser{}).Where("id = ?", userID).Updates(map[string]any{"password_hash": string(hash), "enabled": true})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return db.Where("user_id = ?", userID).Delete(&AdminSession{}).Error
	})
}
