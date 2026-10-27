package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/IEZhu/class/backend/internal/lemma"
	"github.com/IEZhu/class/backend/internal/store"
)

const (
	// Страница словаря (GET /user-terms): по умолчанию и потолок
	defaultUserTermsLimit = 200
	maxUserTermsLimit     = 500
	// maxLookupWords — слов в одном запросе раскраски. В двухчасовом
	// транскрипте 2–4 тыс. разных слов: web режет их на пачки по столько
	maxLookupWords = 2000
	// maxLookupBytes — тело пачки: 2000 слов по 64 байта с кавычками
	// и запятыми
	maxLookupBytes = 1 << 18
)

type saveUserTermRequest struct {
	TermID int64 `json:"term_id"`
	// Status — nil: новое слово получает 1, у существующего статус прежний
	Status         *int   `json:"status"`
	SourceLessonID *int64 `json:"source_lesson_id"`
}

type sourceLessonResponse struct {
	ID int64 `json:"id"`
	// Title и GroupName — для подписи как у урока в списке (lessonTitle в web)
	Title     string    `json:"title"`
	GroupID   *int64    `json:"group_id"`
	GroupName string    `json:"group_name"`
	StartsAt  time.Time `json:"starts_at"`
}

type userTermResponse struct {
	TermID  int64  `json:"term_id"`
	Lemma   string `json:"lemma"`
	Surface string `json:"surface"`
	// Translation — перевод из кэша термина; null — слово не переводили
	Translation  *string               `json:"translation"`
	Status       int                   `json:"status"`
	SourceLesson *sourceLessonResponse `json:"source_lesson"`
	AddedAt      time.Time             `json:"added_at"`
	UpdatedAt    time.Time             `json:"updated_at"`
}

type userTermsPage struct {
	Items []userTermResponse `json:"items"`
	// NextCursor — для ?cursor= следующей страницы; null — страница последняя
	NextCursor *string `json:"next_cursor"`
}

type lookupRequest struct {
	Words []string `json:"words"`
}

// lookupResponse — раскраска текста: Lemmas — лемма слова, только если
// она отличается от самого слова (running → run), Statuses — статус по
// лемме, только для слов из словаря. Статус слова в web —
// statuses[lemmas[w] ?? w]; после смены статуса одного термина web
// перекрашивает все его формы без нового запроса.
type lookupResponse struct {
	Lemmas   map[string]string `json:"lemmas"`
	Statuses map[string]int    `json:"statuses"`
}

func toUserTermResponse(ut *store.UserTerm) userTermResponse {
	resp := userTermResponse{
		TermID: ut.TermID, Lemma: ut.Lemma, Surface: ut.Surface, Translation: ut.Translation,
		Status: ut.Status, AddedAt: ut.AddedAt, UpdatedAt: ut.UpdatedAt,
	}
	if l := ut.SourceLesson; l != nil {
		resp.SourceLesson = &sourceLessonResponse{
			ID: l.ID, Title: l.Title, GroupID: l.GroupID, GroupName: l.GroupName, StartsAt: l.StartsAt,
		}
	}
	return resp
}

func validTermStatus(status int) bool {
	return status >= store.MinTermStatus && status <= store.MaxTermStatus
}

// handleSaveUserTerm — POST /user-terms (S3-5, ADR-027): «в мой словарь»
// и смена статуса. 201 — слово добавлено, 200 — уже было (статус сменён
// или прежний).
func (a *API) handleSaveUserTerm(w http.ResponseWriter, r *http.Request) {
	var req saveUserTermRequest
	if err := decodeBody(w, r, &req); err != nil {
		badBody(w, err, "нужен JSON {term_id, status?, source_lesson_id?}")
		return
	}
	switch {
	case req.TermID <= 0:
		writeError(w, http.StatusBadRequest, "bad_request", "некорректный term_id")
		return
	case req.Status != nil && !validTermStatus(*req.Status):
		writeError(w, http.StatusBadRequest, "bad_request", "status — целое от 1 до 5")
		return
	}
	// Источник — только урок, который студент видит: иначе в словаре
	// всплыла бы подпись чужого урока
	if req.SourceLessonID != nil && !a.checkClickLesson(w, r, *req.SourceLessonID) {
		return
	}
	u := userFrom(r.Context())
	created, err := a.store.SaveUserTerm(r.Context(), u.ID, req.TermID, req.Status, req.SourceLessonID)
	if err != nil {
		if store.PgErrorCode(err) == pgForeignKeyViolation {
			// Термина нет, или урок отменили между проверкой и записью
			writeError(w, http.StatusNotFound, "not_found", "слово или урок не найдены")
			return
		}
		internalError(w, "save user term", err)
		return
	}
	ut, err := a.store.GetUserTerm(r.Context(), u.ID, req.TermID, translationLang)
	if err != nil {
		internalError(w, "get user term", err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, toUserTermResponse(ut))
}

// handleListUserTerms — GET /user-terms?status=1,2&cursor=&limit= —
// словарь студента, новые сверху.
func (a *API) handleListUserTerms(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.UserTermFilter{Limit: defaultUserTermsLimit, Lang: translationLang}
	// status=1,2 и status=1&status=2 — одно и то же
	for _, v := range q["status"] {
		for part := range strings.SplitSeq(v, ",") {
			status, err := strconv.Atoi(strings.TrimSpace(part))
			if err != nil || !validTermStatus(status) {
				writeError(w, http.StatusBadRequest, "bad_request", "status — числа от 1 до 5 через запятую")
				return
			}
			f.Statuses = append(f.Statuses, status)
		}
	}
	if v := q.Get("limit"); v != "" {
		limit, err := strconv.Atoi(v)
		if err != nil || limit < 1 || limit > maxUserTermsLimit {
			writeError(w, http.StatusBadRequest, "bad_request", "limit — от 1 до 500")
			return
		}
		f.Limit = limit
	}
	if v := q.Get("cursor"); v != "" {
		c, ok := parseUserTermCursor(v)
		if !ok {
			writeError(w, http.StatusBadRequest, "bad_request", "некорректный cursor")
			return
		}
		f.After = &c
	}
	// Строка сверх страницы — признак, что следующая страница есть
	limit := f.Limit
	f.Limit++
	items, err := a.store.ListUserTerms(r.Context(), userFrom(r.Context()).ID, f)
	if err != nil {
		internalError(w, "list user terms", err)
		return
	}
	page := userTermsPage{Items: make([]userTermResponse, 0, min(len(items), limit))}
	if len(items) > limit {
		items = items[:limit]
		last := items[limit-1]
		next := formatUserTermCursor(store.UserTermCursor{AddedAt: last.AddedAt, TermID: last.TermID})
		page.NextCursor = &next
	}
	for i := range items {
		page.Items = append(page.Items, toUserTermResponse(&items[i]))
	}
	writeJSON(w, http.StatusOK, page)
}

// Курсор — «микросекунды added_at_term_id»: Postgres хранит время
// с точностью до микросекунды, поэтому позиция восстанавливается точно.
func formatUserTermCursor(c store.UserTermCursor) string {
	return fmt.Sprintf("%d_%d", c.AddedAt.UnixMicro(), c.TermID)
}

func parseUserTermCursor(s string) (store.UserTermCursor, bool) {
	at, id, ok := strings.Cut(s, "_")
	if !ok {
		return store.UserTermCursor{}, false
	}
	micros, err1 := strconv.ParseInt(at, 10, 64)
	termID, err2 := strconv.ParseInt(id, 10, 64)
	if err1 != nil || err2 != nil || termID <= 0 {
		return store.UserTermCursor{}, false
	}
	return store.UserTermCursor{AddedAt: time.UnixMicro(micros).UTC(), TermID: termID}, true
}

// handleDeleteUserTerm — DELETE /user-terms/{term_id}: убрать слово из
// своего словаря. Чужие словари этим путём недостижимы: строка ищется
// по паре (вошедший, термин).
func (a *API) handleDeleteUserTerm(w http.ResponseWriter, r *http.Request) {
	termID, ok := pathIDNamed(w, r, "term_id")
	if !ok {
		return
	}
	err := a.store.DeleteUserTerm(r.Context(), userFrom(r.Context()).ID, termID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "этого слова нет в словаре")
	case err != nil:
		internalError(w, "delete user term", err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleLookupUserTerms — POST /user-terms/lookup {words}: статусы слов
// видимого текста для раскраски ClickableText (ADR-026). Слова
// лемматизируются здесь же, в памяти, а в базу уходит один запрос на
// пачку. Не-слова пропускаются молча: токенизатор web мог разрезать
// текст иначе, раскраска остального текста от этого не страдает.
func (a *API) handleLookupUserTerms(w http.ResponseWriter, r *http.Request) {
	var req lookupRequest
	if err := decodeBodyLimit(w, r, &req, maxLookupBytes); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", "пачка слов больше 256 KiB")
			return
		}
		writeError(w, http.StatusBadRequest, "bad_request", "нужен JSON {words: [...]}")
		return
	}
	if len(req.Words) > maxLookupWords {
		writeError(w, http.StatusBadRequest, "bad_request",
			fmt.Sprintf("не больше %d слов за запрос", maxLookupWords))
		return
	}
	resp := lookupResponse{Lemmas: map[string]string{}, Statuses: map[string]int{}}
	lemmas := make([]string, 0, len(req.Words))
	seen := make(map[string]bool, len(req.Words))
	for _, word := range req.Words {
		if !lemma.IsWord(word) {
			continue
		}
		lem := lemma.Lemma(word)
		if lem != word {
			resp.Lemmas[word] = lem
		}
		if !seen[lem] {
			seen[lem] = true
			lemmas = append(lemmas, lem)
		}
	}
	statuses, err := a.store.UserTermStatusesByLemma(r.Context(), userFrom(r.Context()).ID, termLang, lemmas)
	if err != nil {
		internalError(w, "lookup user terms", err)
		return
	}
	resp.Statuses = statuses
	writeJSON(w, http.StatusOK, resp)
}
