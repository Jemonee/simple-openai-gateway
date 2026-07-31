package gateway

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
)

var (
	ErrInvalidClientToken = errors.New("invalid or revoked API token")
	ErrRateLimitExceeded  = errors.New("API token RPM limit exceeded")
	ErrConcurrencyLimit   = errors.New("API token concurrency limit exceeded")
	ErrModelNotAllowed    = errors.New("API token is not allowed to use this model")
)

type tokenWindow struct {
	startedAt time.Time
	requests  int
	active    int
}

type ClientAccessService struct {
	store   *Store
	mu      sync.Mutex
	windows map[uint64]tokenWindow
}

func NewClientAccessService(store *Store) *ClientAccessService {
	return &ClientAccessService{store: store, windows: make(map[uint64]tokenWindow)}
}

func (s *ClientAccessService) Authenticate(ctx context.Context, authorization string) (*ClientToken, error) {
	prefix, rawToken, ok := strings.Cut(strings.TrimSpace(authorization), " ")
	if !ok || !strings.EqualFold(prefix, "Bearer") || !strings.HasPrefix(rawToken, "sk-") {
		return nil, ErrInvalidClientToken
	}
	var token ClientToken
	if err := s.store.db.WithContext(ctx).Where("key_hash = ? AND enabled = ?", hashSecret(rawToken), true).First(&token).Error; err != nil {
		return nil, ErrInvalidClientToken
	}
	return &token, nil
}

func (s *ClientAccessService) Acquire(token *ClientToken) (func(), error) {
	if token == nil {
		return nil, ErrInvalidClientToken
	}
	now := time.Now()
	s.mu.Lock()
	window := s.windows[token.ID]
	if window.startedAt.IsZero() || now.Sub(window.startedAt) >= time.Minute {
		window.startedAt = now
		window.requests = 0
	}
	if token.RPM > 0 && window.requests >= token.RPM {
		s.mu.Unlock()
		return nil, ErrRateLimitExceeded
	}
	if token.MaxConcurrency > 0 && window.active >= token.MaxConcurrency {
		s.mu.Unlock()
		return nil, ErrConcurrencyLimit
	}
	window.requests++
	window.active++
	s.windows[token.ID] = window
	s.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			current := s.windows[token.ID]
			if current.active > 0 {
				current.active--
			}
			s.windows[token.ID] = current
			s.mu.Unlock()
		})
	}, nil
}

func (s *ClientAccessService) AuthorizeModel(ctx context.Context, token *ClientToken, modelID uint64) error {
	if token == nil || !token.Enabled {
		return ErrInvalidClientToken
	}
	if token.AllowAllModels {
		return nil
	}
	var count int64
	err := s.store.db.WithContext(ctx).Model(&ClientTokenModel{}).
		Where("token_id = ? AND model_id = ?", token.ID, modelID).Count(&count).Error
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrModelNotAllowed
	}
	return nil
}

func (s *ClientAccessService) Touch(ctx context.Context, tokenID uint64) {
	_ = s.store.db.WithContext(ctx).Model(&ClientToken{}).Where("id = ?", tokenID).Update("last_used_at", time.Now()).Error
}

func (s *ClientAccessService) ListModels(ctx context.Context, token *ClientToken) ([]GatewayModel, error) {
	if token == nil {
		return nil, ErrInvalidClientToken
	}
	db := s.store.db.WithContext(ctx).Model(&GatewayModel{}).
		Where("enabled = ?", true).
		Where(`EXISTS (
			SELECT 1
			FROM channel_models AS mapping
			JOIN channels AS channel ON channel.id = mapping.channel_id
			WHERE mapping.model_id = gateway_models.id
				AND mapping.enabled = ?
				AND channel.enabled = ?
				AND (channel.circuit_open_until IS NULL OR channel.circuit_open_until <= ?)
		)`, true, true, time.Now())
	if !token.AllowAllModels {
		db = db.Where("EXISTS (SELECT 1 FROM client_token_models tm WHERE tm.model_id = gateway_models.id AND tm.token_id = ?)", token.ID)
	}
	var models []GatewayModel
	if err := db.Order("name asc").Find(&models).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return []GatewayModel{}, nil
		}
		return nil, err
	}
	return models, nil
}
