// Package httpapi — HTTP-слой api: роуты, middleware, хендлеры.
// Пути без префикса /api: его стрипает caddy (docs/architecture/04-api.md).
package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/IEZhu/class/backend/internal/livekitapi"
	"github.com/IEZhu/class/backend/internal/storage"
	"github.com/IEZhu/class/backend/internal/store"
)

// Config — внешние параметры api, не выводимые из БД.
type Config struct {
	// PublicBaseURL — внешний адрес стенда (https://<DOMAIN>), из которого
	// собираются ссылки-приглашения (ADR-008).
	PublicBaseURL string
	// LiveKit (S1-4): пустые значения не роняют api — комната отдаёт 503,
	// остальной стенд работает.
	LiveKitURL       string
	LiveKitAPIKey    string
	LiveKitAPISecret string
	// EgressAudioOnly — писать только звук: на порядок дешевле по объёму,
	// для транскрипта этого достаточно (05-storage-s3.md).
	EgressAudioOnly bool
	Storage         storage.Config
}

// liveKitConfigured — заданы все три ключа LiveKit. Без URL некуда
// отправить ни браузер, ни запрос Egress; без ключей — нечем подписать.
func (c Config) liveKitConfigured() bool {
	return c.LiveKitURL != "" && c.LiveKitAPIKey != "" && c.LiveKitAPISecret != ""
}

// objectStore — то, что api нужно от хранилища (storage.Client). Через
// интерфейс тесты подставляют фейк вместо R2.
type objectStore interface {
	PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error)
	PresignPut(ctx context.Context, key, contentType, contentDisposition string, size int64, ttl time.Duration) (storage.PresignedPut, error)
	Stat(ctx context.Context, key string) (storage.ObjectInfo, error)
	Copy(ctx context.Context, src, dst string) error
	Delete(ctx context.Context, key string) error
}

type API struct {
	store   *store.Store
	cfg     Config
	storage objectStore // nil, если хранилище не настроено
	livekit *livekitapi.Client
}

// New не падает без ключей интеграций: стенд должен подниматься, а
// эндпоинты записи и комнаты — отвечать 503.
func New(s *store.Store, cfg Config) (*API, error) {
	st, err := storage.New(cfg.Storage)
	if err != nil {
		return nil, err
	}
	a := &API{
		store:   s,
		cfg:     cfg,
		livekit: livekitapi.New(cfg.LiveKitURL, cfg.LiveKitAPIKey, cfg.LiveKitAPISecret),
	}
	// Без ключей storage.New отдаёт nil-указатель; в интерфейс его класть
	// нельзя — проверка a.storage == nil перестала бы его видеть
	if st != nil {
		a.storage = st
	}
	return a, nil
}

func (a *API) Router() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", a.handleHealthz)
	mux.HandleFunc("POST /auth/login", a.handleLogin)
	mux.HandleFunc("POST /auth/logout", a.handleLogout)
	mux.Handle("GET /auth/me", a.requireUser(http.HandlerFunc(a.handleMe)))

	user := func(h http.HandlerFunc) http.Handler { return a.requireUser(h) }
	teacher := func(h http.HandlerFunc) http.Handler { return a.requireAnyRole(h, store.RoleTeacher) }
	admin := func(h http.HandlerFunc) http.Handler { return a.requireAnyRole(h, store.RoleAdmin) }
	// staff — админ и преподаватель: люди и чтение групп. Границу «чужой
	// студент» внутри staff проверяет хендлер (ADR-007), одной роли мало.
	staff := func(h http.HandlerFunc) http.Handler {
		return a.requireAnyRole(h, store.RoleAdmin, store.RoleTeacher)
	}

	mux.Handle("PATCH /auth/me", user(a.handleUpdateMe))
	mux.Handle("POST /auth/password", user(a.handleChangeOwnPassword))

	// Публичные: по ссылке-приглашению человек ещё не залогинен (ADR-008)
	mux.HandleFunc("GET /signup/{token}", a.handleInvitePreview)
	mux.HandleFunc("POST /signup/{token}", a.handleAcceptInvite)

	// Публичные: гость на событие представляется именем (ADR-010)
	mux.HandleFunc("GET /join/{token}", a.handleGuestEvent)
	mux.HandleFunc("POST /join/{token}", a.handleGuestJoin)

	mux.Handle("GET /invites", staff(a.handleListInvites))
	mux.Handle("POST /invites", staff(a.handleCreateInvite))
	mux.Handle("DELETE /invites/{id}", staff(a.handleRevokeInvite))

	mux.Handle("GET /users", staff(a.handleListUsers))
	mux.Handle("POST /users", staff(a.handleCreateUser))
	mux.Handle("PATCH /users/{id}", staff(a.handleUpdateUser))
	mux.Handle("POST /users/{id}/password", staff(a.handleResetPassword))

	// Состав групп — только админ. У groups нет владельца, поэтому «своя
	// группа» для записи неопределима: преподаватель добавил бы себя в любую
	// и получил бы её студентов. Чтение оставляем staff — нужен состав
	// на уроке (ADR-007).
	mux.Handle("POST /groups", admin(a.handleCreateGroup))
	mux.Handle("GET /groups", staff(a.handleListGroups))
	mux.Handle("POST /groups/{id}/members", admin(a.handleAddGroupMember))
	mux.Handle("DELETE /groups/{id}/members/{user_id}", admin(a.handleRemoveGroupMember))

	mux.Handle("POST /lessons", teacher(a.handleCreateLesson))
	mux.Handle("GET /lessons", user(a.handleListLessons))
	mux.Handle("GET /lessons/{id}", user(a.handleGetLesson))
	mux.Handle("GET /lessons/{id}/room-token", user(a.handleRoomToken))
	mux.Handle("GET /media/{lesson_id}/url", user(a.handleMediaURL))

	// Гостевая ссылка на событие: распоряжается преподаватель или админ
	mux.Handle("POST /lessons/{id}/guest-link", staff(a.handleIssueGuestLink))
	mux.Handle("DELETE /lessons/{id}/guest-link", staff(a.handleRevokeGuestLink))

	// Вебхук LiveKit без auth-middleware: он аутентифицируется подписью
	mux.HandleFunc("POST /webhooks/livekit", a.handleLiveKitWebhook)
	mux.Handle("PATCH /lessons/{id}", teacher(a.handleRescheduleLesson))
	mux.Handle("DELETE /lessons/{id}", teacher(a.handleCancelLesson))

	mux.Handle("POST /lessons/{id}/materials", teacher(a.handleCreateMaterial("material")))
	mux.Handle("POST /lessons/{id}/homework", teacher(a.handleCreateMaterial("homework")))

	// Долги D-4 и D-5 (ADR-024): файлы материалов в R2 и архивация учёток.
	// Файл грузит браузер по presigned PUT, api только выдаёт ссылку,
	// сверяет объект и подписывает скачивание.
	mux.Handle("POST /materials/{id}/confirm", teacher(a.handleConfirmMaterialFile))
	mux.Handle("GET /materials/{id}/url", user(a.handleMaterialFileURL))
	mux.Handle("DELETE /materials/{id}", teacher(a.handleDeleteMaterial))
	mux.Handle("POST /users/{id}/deactivate", staff(a.handleSetUserArchived(true)))
	mux.Handle("POST /users/{id}/reactivate", staff(a.handleSetUserArchived(false)))
	return mux
}

const (
	maxBodyBytes = 1 << 16 // 64 KiB — с запасом для любого JSON-пейлоада этапа 0

	// SQLSTATE-коды Postgres для маппинга в HTTP (store.PgErrorCode)
	pgForeignKeyViolation = "23503"
	pgUniqueViolation     = "23505"
	pgCheckViolation      = "23514"
)

// internalError — единый ответ 500 + лог реальной причины.
func internalError(w http.ResponseWriter, op string, err error) {
	logf("%s: %v", op, err)
	writeError(w, http.StatusInternalServerError, "internal", "внутренняя ошибка")
}

func (a *API) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		if err := json.NewEncoder(w).Encode(v); err != nil {
			log.Printf("httpapi: encode response: %v", err)
		}
	}
}

// writeError — формат ошибок {error, code} из 04-api.md.
func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
}

// newToken — 256-битный случайный токен и его sha256-дайджест: в БД
// уходит только дайджест (сессии — ADR-006, приглашения — ADR-008).
func newToken() (token, tokenHash string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	token = hex.EncodeToString(b)
	return token, hashToken(token), nil
}

// hashToken — в БД хранится только sha256-дайджест токена (ADR-006).
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
