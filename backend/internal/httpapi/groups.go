package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/IEZhu/class/backend/internal/store"
)

type groupResponse struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Level string `json:"level"`
	// Без omitempty: у пустой группы поле должно приезжать как [], иначе
	// клиент получает undefined там, где по контракту массив.
	Members []memberResponse `json:"members"`
}

type memberResponse struct {
	UserID int64  `json:"user_id"`
	Email  string `json:"email"`
	Name   string `json:"name"`
	Role   string `json:"role"`
}

func toMemberResponses(ms []store.GroupMember) []memberResponse {
	out := make([]memberResponse, 0, len(ms))
	for _, m := range ms {
		out = append(out, memberResponse{UserID: m.UserID, Email: m.Email, Name: m.Name, Role: m.Role})
	}
	return out
}

func (a *API) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name  string `json:"name"`
		Level string `json:"level"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		badBody(w, err, "невалидный JSON")
		return
	}
	if req.Name == "" || req.Level == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "name и level обязательны")
		return
	}
	g, err := a.store.CreateGroup(r.Context(), req.Name, req.Level)
	if err != nil {
		if store.PgErrorCode(err) == pgCheckViolation {
			writeError(w, http.StatusBadRequest, "bad_request", "level — CEFR: A1, A2, B1, B2, C1, C2")
			return
		}
		internalError(w, "create group", err)
		return
	}
	writeJSON(w, http.StatusCreated, groupResponse{
		ID: g.ID, Name: g.Name, Level: g.Level, Members: []memberResponse{},
	})
}

func (a *API) handleListGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := a.store.ListGroupsWithMembers(r.Context())
	if err != nil {
		internalError(w, "list groups", err)
		return
	}
	out := make([]groupResponse, 0, len(groups))
	for _, g := range groups {
		out = append(out, groupResponse{ID: g.ID, Name: g.Name, Level: g.Level, Members: toMemberResponses(g.Members)})
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleAddGroupMember(w http.ResponseWriter, r *http.Request) {
	groupID, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Email string `json:"email"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		badBody(w, err, "невалидный JSON")
		return
	}
	if req.Email == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "email обязателен")
		return
	}
	m, err := a.store.AddGroupMemberByEmail(r.Context(), groupID, req.Email)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "пользователь с таким email не найден")
	case store.PgErrorCode(err) == pgForeignKeyViolation:
		writeError(w, http.StatusNotFound, "not_found", "группа не найдена")
	case err != nil:
		internalError(w, "add group member", err)
	default:
		writeJSON(w, http.StatusCreated, memberResponse{UserID: m.UserID, Email: m.Email, Name: m.Name, Role: m.Role})
	}
}

// decodeBody — общий декодер JSON-тел: лимит размера и ровно одно
// JSON-значение (хвост после первого значения — ошибка, иначе он
// обходил бы лимит MaxBytesReader).
func decodeBody(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain exactly one JSON value")
		}
		return err
	}
	return nil
}

// badBody — ответ на невалидное тело: 413 при превышении лимита, иначе 400.
func badBody(w http.ResponseWriter, err error, msg string) {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "тело запроса больше 64 KiB")
		return
	}
	writeError(w, http.StatusBadRequest, "bad_request", msg)
}
