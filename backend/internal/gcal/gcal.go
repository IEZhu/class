// Package gcal — Google Calendar преподавателя (S1-1, ADR-023): OAuth2
// web flow, шифрование refresh token для users.google_refresh_token и три
// вызова Calendar API v3 — events insert/update/delete.
//
// Полный клиент google.golang.org/api не берём: от него нужны три REST-вызова
// и отзыв токена, а тянет он gRPC, OpenTelemetry и сотни сгенерированных
// пакетов. Токены и их обновление — golang.org/x/oauth2 (корневой пакет,
// без x/oauth2/google: тот тянет cloud.google.com/go/compute/metadata ради
// адресов, которые проще записать здесь).
package gcal

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// Scope — только события календарей: ни настроек, ни списка календарей.
const Scope = "https://www.googleapis.com/auth/calendar.events"

// Боевые адреса Google; Config позволяет подменить их фейком в тестах.
const (
	defaultAuthURL     = "https://accounts.google.com/o/oauth2/v2/auth"
	defaultTokenURL    = "https://oauth2.googleapis.com/token"
	defaultRevokeURL   = "https://oauth2.googleapis.com/revoke"
	defaultCalendarURL = "https://www.googleapis.com/calendar/v3"

	// requestTimeout — потолок любого вызова Google, включая обновление
	// access token: зависший Google не должен держать хендлер или джобу.
	requestTimeout = 15 * time.Second
)

var (
	// ErrBadTokenKey — GOOGLE_TOKEN_KEY задан, но это не ключ AES-256.
	ErrBadTokenKey = errors.New("gcal: GOOGLE_TOKEN_KEY — base64 от 32 случайных байт (openssl rand -base64 32)")
	// ErrScopeDenied — человек снял галочку календаря на экране согласия.
	ErrScopeDenied = errors.New("gcal: доступ к событиям календаря не выдан")
	// ErrNoRefreshToken — Google не вернул refresh token: без него
	// worker не сможет работать от имени преподавателя.
	ErrNoRefreshToken = errors.New("gcal: в ответе Google нет refresh token")
	// ErrRevoked — refresh token отозван в аккаунте Google или истёк
	// (invalid_grant): повтор не поможет, нужно подключиться заново.
	ErrRevoked = errors.New("gcal: доступ к календарю отозван")
	// ErrNotFound — события нет (404) или оно уже удалено (410).
	ErrNotFound = errors.New("gcal: событие не найдено")
	// ErrConflict — событие с этим id уже есть (409 duplicate).
	ErrConflict = errors.New("gcal: событие с таким id уже есть")
)

// Config — ключи из deploy/.env и адреса Google.
type Config struct {
	ClientID     string
	ClientSecret string
	// TokenKey — base64 от 32 байт, ключ AES-256-GCM для refresh token.
	TokenKey string
	// RedirectURL — https://<DOMAIN>/api/auth/google/callback; нужен только
	// api, worker обновляет токены без редиректа.
	RedirectURL string
	// Пустые — боевые адреса Google.
	AuthURL     string
	TokenURL    string
	RevokeURL   string
	CalendarURL string
}

// Missing — какие из трёх ключей не заданы, для строки в журнале.
func (c Config) Missing() []string {
	var out []string
	for _, k := range []struct{ name, value string }{
		{"GOOGLE_CLIENT_ID", c.ClientID},
		{"GOOGLE_CLIENT_SECRET", c.ClientSecret},
		{"GOOGLE_TOKEN_KEY", c.TokenKey},
	} {
		if k.value == "" {
			out = append(out, k.name)
		}
	}
	return out
}

type Client struct {
	oauth       oauth2.Config
	aead        cipher.AEAD
	revokeURL   string
	calendarURL string
	// http — для обмена кода, обновления access token и отзыва: у этих
	// запросов потолок — Timeout клиента.
	http *http.Client
	// timeout — потолок одного вызова Calendar API (Calendar.do);
	// requestTimeout, тесты ставят короче.
	timeout time.Duration
}

// New возвращает nil без ошибки, пока задан не каждый ключ: стенд
// поднимается и без Google, подключение отвечает 503, а calendar_sync
// пропускает уроки (как storage.New). Ошибка — только на битый ключ.
func New(cfg Config) (*Client, error) {
	if len(cfg.Missing()) > 0 {
		return nil, nil
	}
	key, err := base64.StdEncoding.DecodeString(cfg.TokenKey)
	if err != nil || len(key) != 32 {
		return nil, ErrBadTokenKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Client{
		oauth: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURL,
			Scopes:       []string{Scope},
			Endpoint: oauth2.Endpoint{
				AuthURL:  or(cfg.AuthURL, defaultAuthURL),
				TokenURL: or(cfg.TokenURL, defaultTokenURL),
				// Google принимает секрет в теле; без явного стиля
				// x/oauth2 сначала пробует заголовок и тратит лишний запрос
				AuthStyle: oauth2.AuthStyleInParams,
			},
		},
		aead:        aead,
		revokeURL:   or(cfg.RevokeURL, defaultRevokeURL),
		calendarURL: strings.TrimSuffix(or(cfg.CalendarURL, defaultCalendarURL), "/"),
		http:        &http.Client{Timeout: requestTimeout},
		timeout:     requestTimeout,
	}, nil
}

func or(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// withHTTP — x/oauth2 берёт HTTP-клиент из контекста: так обмен кода
// и обновление токена получают тот же таймаут.
func (c *Client) withHTTP(ctx context.Context) context.Context {
	return context.WithValue(ctx, oauth2.HTTPClient, c.http)
}

// AuthCodeURL — адрес экрана согласия и PKCE-verifier к нему (ADR-023).
// access_type=offline даёт refresh token; prompt=consent — даёт его и при
// повторном подключении, иначе Google присылает только access token.
// state и verifier api держит в cookie до возврата на callback (SealFlow).
func (c *Client) AuthCodeURL(state string) (authURL, verifier string) {
	verifier = oauth2.GenerateVerifier()
	return c.oauth.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.ApprovalForce,
		oauth2.S256ChallengeOption(verifier)), verifier
}

// Exchange меняет код с callback на refresh token. Экран согласия Google
// позволяет выдать не все запрошенные доступы, поэтому выданный scope
// проверяется явно.
func (c *Client) Exchange(ctx context.Context, code, verifier string) (string, error) {
	tok, err := c.oauth.Exchange(c.withHTTP(ctx), code, oauth2.VerifierOption(verifier))
	if err != nil {
		return "", fmt.Errorf("gcal: обмен кода: %w", err)
	}
	granted, _ := tok.Extra("scope").(string)
	if !slices.Contains(strings.Fields(granted), Scope) {
		return "", ErrScopeDenied
	}
	if tok.RefreshToken == "" {
		return "", ErrNoRefreshToken
	}
	return tok.RefreshToken, nil
}

// Revoke отзывает токен в Google: у отключённого календаря не должно
// остаться рабочей копии токена, например в бэкапе БД. Потолок — Timeout
// клиента, поэтому вызывающий может отвязать ctx от запроса браузера.
func (c *Client) Revoke(ctx context.Context, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.revokeURL,
		strings.NewReader(url.Values{"token": {token}}.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("gcal: отзыв токена: %w", err)
	}
	defer res.Body.Close() //nolint:errcheck // тело только читаем
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("gcal: отзыв токена: %s", responseError(res))
	}
	return nil
}

// sealVersion — первый байт шифртекста: формат можно будет сменить,
// не гадая, чем зашифрована старая строка.
const sealVersion = 1

// Seal шифрует refresh token для users.google_refresh_token: AES-256-GCM,
// версия ‖ nonce ‖ шифртекст с тегом. id пользователя — в associated data:
// шифртекст, перенесённый в чужую строку, не расшифруется.
func (c *Client) Seal(userID int64, token string) ([]byte, error) {
	return c.seal(tokenAAD(userID), []byte(token))
}

// Open — обратное к Seal. Ошибка — другой ключ (сменили GOOGLE_TOKEN_KEY),
// чужая строка или порча.
func (c *Client) Open(userID int64, sealed []byte) (string, error) {
	plain, err := c.open(tokenAAD(userID), sealed)
	if err != nil {
		return "", fmt.Errorf("gcal: токен не расшифровывается: %w", err)
	}
	return string(plain), nil
}

// Flow — state и PKCE-verifier одного подключения: api держит их в cookie,
// пока преподаватель на экране согласия Google (ADR-023).
type Flow struct {
	State    string    `json:"state"`
	Verifier string    `json:"verifier"`
	Expires  time.Time `json:"expires"`
}

// SealFlow — значение cookie для Flow: тот же AES-256-GCM, что у токенов,
// с id преподавателя в associated data. Cookie, подложенный чужим сайтом
// или выданный другой учётке, не откроется: без ключа его не собрать.
func (c *Client) SealFlow(userID int64, f Flow) (string, error) {
	plain, err := json.Marshal(f)
	if err != nil {
		return "", err
	}
	sealed, err := c.seal(flowAAD(userID), plain)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

// OpenFlow — обратное к SealFlow. Ошибка — cookie чужой, испорчен или
// старше Flow.Expires на момент now.
func (c *Client) OpenFlow(userID int64, value string, now time.Time) (Flow, error) {
	var f Flow
	sealed, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return f, fmt.Errorf("gcal: cookie подключения: %w", err)
	}
	plain, err := c.open(flowAAD(userID), sealed)
	if err != nil {
		return f, fmt.Errorf("gcal: cookie подключения не расшифровывается: %w", err)
	}
	if err := json.Unmarshal(plain, &f); err != nil {
		return f, fmt.Errorf("gcal: cookie подключения: %w", err)
	}
	if !now.Before(f.Expires) {
		return f, errors.New("gcal: cookie подключения истёк")
	}
	return f, nil
}

// seal — версия ‖ nonce ‖ шифртекст с тегом; aad разводит назначения
// одного ключа: токен из cookie не откроется как токен из users и наоборот.
func (c *Client) seal(aad, plain []byte) ([]byte, error) {
	out := make([]byte, 1+c.aead.NonceSize(), 1+c.aead.NonceSize()+len(plain)+c.aead.Overhead())
	out[0] = sealVersion
	if _, err := io.ReadFull(rand.Reader, out[1:]); err != nil {
		return nil, err
	}
	return c.aead.Seal(out, out[1:], plain, aad), nil
}

func (c *Client) open(aad, sealed []byte) ([]byte, error) {
	n := c.aead.NonceSize()
	if len(sealed) < 1+n || sealed[0] != sealVersion {
		return nil, errors.New("gcal: неизвестный формат шифртекста")
	}
	return c.aead.Open(nil, sealed[1:1+n], sealed[1+n:], aad)
}

func tokenAAD(userID int64) []byte {
	return []byte("users.google_refresh_token:" + strconv.FormatInt(userID, 10))
}

func flowAAD(userID int64) []byte {
	return []byte("google_oauth:" + strconv.FormatInt(userID, 10))
}

func responseError(res *http.Response) string {
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 512))
	return strings.TrimSpace(res.Status + " " + string(raw))
}
