package gateway

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

func httpClientWithProxy(proxyURL string, timeout time.Duration) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if strings.TrimSpace(proxyURL) != "" {
		parsed, err := url.Parse(strings.TrimSpace(proxyURL))
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return nil, errors.New("invalid proxy URL")
		}
		switch strings.ToLower(parsed.Scheme) {
		case "http", "https":
			transport.Proxy = http.ProxyURL(parsed)
		case "socks5", "socks5h":
			var auth *proxy.Auth
			if parsed.User != nil {
				password, _ := parsed.User.Password()
				auth = &proxy.Auth{User: parsed.User.Username(), Password: password}
			}
			dialer, dialErr := proxy.SOCKS5("tcp", parsed.Host, auth, proxy.Direct)
			if dialErr != nil {
				return nil, fmt.Errorf("configure SOCKS5 proxy: %w", dialErr)
			}
			transport.Proxy = nil
			transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				if contextDialer, ok := dialer.(proxy.ContextDialer); ok {
					return contextDialer.DialContext(ctx, network, address)
				}
				return dialer.Dial(network, address)
			}
		default:
			return nil, errors.New("proxy URL must use HTTP, HTTPS, SOCKS5, or SOCKS5H")
		}
	}
	return &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

const (
	ChatGPTCodexBaseURL       = "https://chatgpt.com/backend-api/codex"
	chatGPTOAuthClientID      = "app_EMoamEEZ73f0CkXaXp7hrann"
	chatGPTOAuthDefaultScopes = "openid profile email offline_access"
	chatGPTOAuthRefreshScopes = "openid profile email"
	chatGPTOAuthSessionTTL    = 30 * time.Minute
	chatGPTCanonicalVersion   = "0.147.0"
	chatGPTCanonicalOrigin    = "codex_cli_rs"
	chatGPTCanonicalUA        = "codex_cli_rs/0.147.0 (Darwin; arm64) xterm-256color"
)

var (
	chatGPTOAuthAuthorizeEndpoint = "https://auth.openai.com/oauth/authorize"
	chatGPTOAuthTokenEndpoint     = "https://auth.openai.com/oauth/token"
)

type chatGPTOAuthSession struct {
	State        string
	CodeVerifier string
	RedirectURI  string
	ProxyURL     string
	CreatedAt    time.Time
}

// ChatGPTOAuthService owns the short-lived PKCE state used while an
// administrator connects a ChatGPT subscription account.
type ChatGPTOAuthService struct {
	mu           sync.Mutex
	sessions     map[string]chatGPTOAuthSession
	client       *http.Client
	authorizeURL string
	tokenURL     string
}

// ChatGPTOAuthStartInput describes the local callback address used by the
// administrator's OAuth client.
type ChatGPTOAuthStartInput struct {
	RedirectURI string `json:"redirectUri"`
	ProxyURL    string `json:"proxyUrl"`
}

// ChatGPTOAuthStartView contains the OpenAI authorization URL and the opaque
// local session identifier required during code exchange.
type ChatGPTOAuthStartView struct {
	AuthorizationURL string `json:"authorizationUrl"`
	SessionID        string `json:"sessionId"`
	State            string `json:"state"`
	RedirectURI      string `json:"redirectUri"`
}

// ChatGPTOAuthExchangeInput completes an OAuth session after the browser has
// returned an authorization code.
type ChatGPTOAuthExchangeInput struct {
	SessionID string `json:"sessionId"`
	Code      string `json:"code"`
	State     string `json:"state"`
}

// ChatGPTOAuthCredential is returned once and is intended to be submitted
// directly to the encrypted channel-configuration endpoint.
type ChatGPTOAuthCredential struct {
	AccessToken      string     `json:"accessToken"`
	RefreshToken     string     `json:"refreshToken"`
	ExpiresAt        *time.Time `json:"expiresAt"`
	Email            string     `json:"email"`
	ChatGPTAccountID string     `json:"chatgptAccountId"`
	ChatGPTUserID    string     `json:"chatgptUserId"`
	ChatGPTPlanType  string     `json:"chatgptPlanType"`
}

type chatGPTOAuthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

type chatGPTTokenClaims struct {
	Email      string `json:"email"`
	OpenAIAuth struct {
		ChatGPTAccountID string `json:"chatgpt_account_id"`
		ChatGPTUserID    string `json:"chatgpt_user_id"`
		ChatGPTPlanType  string `json:"chatgpt_plan_type"`
	} `json:"https://api.openai.com/auth"`
}

func NewChatGPTOAuthService() *ChatGPTOAuthService {
	return &ChatGPTOAuthService{
		sessions:     make(map[string]chatGPTOAuthSession),
		client:       &http.Client{Timeout: 30 * time.Second},
		authorizeURL: chatGPTOAuthAuthorizeEndpoint,
		tokenURL:     chatGPTOAuthTokenEndpoint,
	}
}

func (s *ChatGPTOAuthService) Start(input ChatGPTOAuthStartInput) (*ChatGPTOAuthStartView, error) {
	redirectURI, err := validateOAuthRedirectURI(input.RedirectURI)
	if err != nil {
		return nil, err
	}
	proxyURL, err := normalizeProxyURL(input.ProxyURL)
	if err != nil {
		return nil, err
	}
	state, err := randomURLToken(32)
	if err != nil {
		return nil, err
	}
	sessionID, err := randomURLToken(24)
	if err != nil {
		return nil, err
	}
	verifier, err := randomURLToken(64)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(digest[:])

	now := time.Now().UTC()
	s.mu.Lock()
	for id, session := range s.sessions {
		if now.Sub(session.CreatedAt) > chatGPTOAuthSessionTTL {
			delete(s.sessions, id)
		}
	}
	s.sessions[sessionID] = chatGPTOAuthSession{State: state, CodeVerifier: verifier, RedirectURI: redirectURI, ProxyURL: proxyURL, CreatedAt: now}
	s.mu.Unlock()

	query := url.Values{
		"response_type":              {"code"},
		"client_id":                  {chatGPTOAuthClientID},
		"redirect_uri":               {redirectURI},
		"scope":                      {chatGPTOAuthDefaultScopes},
		"state":                      {state},
		"code_challenge":             {challenge},
		"code_challenge_method":      {"S256"},
		"id_token_add_organizations": {"true"},
		"codex_cli_simplified_flow":  {"true"},
	}
	return &ChatGPTOAuthStartView{
		AuthorizationURL: s.authorizeURL + "?" + query.Encode(),
		SessionID:        sessionID,
		State:            state,
		RedirectURI:      redirectURI,
	}, nil
}

func (s *ChatGPTOAuthService) Exchange(ctx context.Context, input ChatGPTOAuthExchangeInput) (*ChatGPTOAuthCredential, error) {
	input.SessionID = strings.TrimSpace(input.SessionID)
	input.Code = strings.TrimSpace(input.Code)
	input.State = strings.TrimSpace(input.State)
	if input.SessionID == "" || input.Code == "" || input.State == "" {
		return nil, errors.New("OAuth session, code, and state are required")
	}

	s.mu.Lock()
	session, ok := s.sessions[input.SessionID]
	if ok && time.Since(session.CreatedAt) > chatGPTOAuthSessionTTL {
		delete(s.sessions, input.SessionID)
		ok = false
	}
	s.mu.Unlock()
	if !ok {
		return nil, errors.New("OAuth session was not found or has expired")
	}
	if subtle.ConstantTimeCompare([]byte(input.State), []byte(session.State)) != 1 {
		return nil, errors.New("OAuth state does not match")
	}

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {chatGPTOAuthClientID},
		"code":          {input.Code},
		"redirect_uri":  {session.RedirectURI},
		"code_verifier": {session.CodeVerifier},
	}
	client := s.client
	var err error
	if session.ProxyURL != "" {
		client, err = httpClientWithProxy(session.ProxyURL, 30*time.Second)
		if err != nil {
			return nil, err
		}
	}
	token, err := requestChatGPTOAuthToken(ctx, client, s.tokenURL, form)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	delete(s.sessions, input.SessionID)
	s.mu.Unlock()
	return chatGPTOAuthCredentialFromToken(token), nil
}

func validateOAuthRedirectURI(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("redirect URI must be an absolute URL without query parameters or a fragment")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("redirect URI must use HTTP or HTTPS")
	}
	if parsed.User != nil {
		return "", errors.New("redirect URI cannot contain user info")
	}
	if parsed.Scheme == "http" {
		host := parsed.Hostname()
		if host != "localhost" && host != "127.0.0.1" && host != "::1" {
			return "", errors.New("an HTTP redirect URI must use localhost")
		}
	}
	return parsed.String(), nil
}

func randomURLToken(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func requestChatGPTOAuthToken(ctx context.Context, client *http.Client, endpoint string, form url.Values) (chatGPTOAuthTokenResponse, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return chatGPTOAuthTokenResponse{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", chatGPTCanonicalUA)
	request.Header.Set("originator", chatGPTCanonicalOrigin)
	response, err := client.Do(request)
	if err != nil {
		return chatGPTOAuthTokenResponse{}, fmt.Errorf("OpenAI OAuth request failed: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return chatGPTOAuthTokenResponse{}, fmt.Errorf("read OpenAI OAuth response: %w", err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return chatGPTOAuthTokenResponse{}, fmt.Errorf("OpenAI OAuth returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	var token chatGPTOAuthTokenResponse
	if err := json.Unmarshal(body, &token); err != nil || strings.TrimSpace(token.AccessToken) == "" {
		return chatGPTOAuthTokenResponse{}, errors.New("OpenAI OAuth returned an invalid token response")
	}
	return token, nil
}

func refreshChatGPTOAuthToken(ctx context.Context, client *http.Client, refreshToken string) (chatGPTOAuthTokenResponse, error) {
	return refreshChatGPTOAuthTokenWithEndpoint(ctx, client, chatGPTOAuthTokenEndpoint, refreshToken)
}

func refreshChatGPTOAuthTokenWithEndpoint(ctx context.Context, client *http.Client, endpoint string, refreshToken string) (chatGPTOAuthTokenResponse, error) {
	return requestChatGPTOAuthToken(ctx, client, endpoint, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {chatGPTOAuthClientID},
		"scope":         {chatGPTOAuthRefreshScopes},
	})
}

func chatGPTOAuthCredentialFromToken(token chatGPTOAuthTokenResponse) *ChatGPTOAuthCredential {
	credential := &ChatGPTOAuthCredential{AccessToken: token.AccessToken, RefreshToken: token.RefreshToken}
	if token.ExpiresIn > 0 {
		expiresAt := time.Now().UTC().Add(time.Duration(token.ExpiresIn) * time.Second)
		credential.ExpiresAt = &expiresAt
	}
	claims := decodeChatGPTTokenClaims(token.IDToken)
	credential.Email = claims.Email
	credential.ChatGPTAccountID = claims.OpenAIAuth.ChatGPTAccountID
	credential.ChatGPTUserID = claims.OpenAIAuth.ChatGPTUserID
	credential.ChatGPTPlanType = claims.OpenAIAuth.ChatGPTPlanType
	return credential
}

func decodeChatGPTTokenClaims(token string) chatGPTTokenClaims {
	var claims chatGPTTokenClaims
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return claims
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		payload, err = base64.URLEncoding.DecodeString(parts[1])
	}
	if err == nil {
		_ = json.Unmarshal(payload, &claims)
	}
	return claims
}

func (s *RelayService) channelAccessToken(ctx context.Context, channel Channel) (string, error) {
	accessToken, err := s.store.secretBox.Decrypt(channel.APIKeyCipher)
	if err != nil {
		return "", err
	}
	if channel.AuthType != ChannelTypeChatGPTOAuth {
		return accessToken, nil
	}
	if (channel.OAuthExpiresAt == nil && strings.TrimSpace(accessToken) != "") || (channel.OAuthExpiresAt != nil && time.Until(*channel.OAuthExpiresAt) > time.Minute) {
		return accessToken, nil
	}
	if channel.ID == 0 {
		return "", errors.New("ChatGPT OAuth channel has no persistent ID")
	}
	lockValue, _ := s.oauthLocks.LoadOrStore(channel.ID, &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	// Another request may have refreshed this channel while this request was
	// waiting for the per-channel lock. Re-read the credential and expiry.
	var current Channel
	if err := s.store.db.WithContext(ctx).First(&current, channel.ID).Error; err != nil {
		return "", err
	}
	accessToken, err = s.store.secretBox.Decrypt(current.APIKeyCipher)
	if err != nil {
		return "", err
	}
	if (current.OAuthExpiresAt == nil && strings.TrimSpace(accessToken) != "") || (current.OAuthExpiresAt != nil && time.Until(*current.OAuthExpiresAt) > time.Minute) {
		return accessToken, nil
	}
	if strings.TrimSpace(current.RefreshTokenCipher) == "" {
		return "", errors.New("ChatGPT OAuth refresh token is missing or expired")
	}
	refreshToken, err := s.store.secretBox.Decrypt(current.RefreshTokenCipher)
	if err != nil {
		return "", err
	}
	client := s.client
	if strings.TrimSpace(current.ProxyURLCipher) != "" {
		proxyURL, proxyErr := s.store.secretBox.Decrypt(current.ProxyURLCipher)
		if proxyErr != nil {
			return "", proxyErr
		}
		client, proxyErr = httpClientWithProxy(proxyURL, 30*time.Second)
		if proxyErr != nil {
			return "", proxyErr
		}
	}
	token, err := refreshChatGPTOAuthToken(ctx, client, refreshToken)
	if err != nil {
		return "", err
	}
	accessCipher, err := s.store.secretBox.Encrypt(token.AccessToken)
	if err != nil {
		return "", err
	}
	updates := map[string]any{"api_key_cipher": accessCipher}
	if token.RefreshToken != "" {
		refreshCipher, encryptErr := s.store.secretBox.Encrypt(token.RefreshToken)
		if encryptErr != nil {
			return "", encryptErr
		}
		updates["refresh_token_cipher"] = refreshCipher
	}
	if token.ExpiresIn > 0 {
		expiresAt := time.Now().UTC().Add(time.Duration(token.ExpiresIn) * time.Second)
		updates["oauth_expires_at"] = expiresAt
	}
	if err := s.store.db.WithContext(ctx).Model(&Channel{}).Where("id = ?", channel.ID).Updates(updates).Error; err != nil {
		return "", err
	}
	return token.AccessToken, nil
}
