package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/IEZhu/class/backend/internal/srs"
	"github.com/IEZhu/class/backend/internal/store"
)

const (
	// Карточек в одной выдаче очереди (GET /practice/queue): по умолчанию
	// и потолок. На каждую — запрос дефиниций и примеров, поэтому выдача
	// короткая, а следующую web берёт, когда пройдена эта
	defaultQueueLimit = 20
	maxQueueLimit     = 50
	// queueDefinitions — статей словаря на обороте карточки
	queueDefinitions = 2
)

// cardResponse — состояние карточки FSRS для web: этап, следующий показ,
// интервал и счётчики.
type cardResponse struct {
	State         srs.State `json:"state"`
	DueAt         time.Time `json:"due_at"`
	ScheduledDays int       `json:"scheduled_days"`
	Reps          int       `json:"reps"`
	Lapses        int       `json:"lapses"`
	// LastReviewAt — null у карточки без повторений
	LastReviewAt *time.Time `json:"last_review_at"`
}

func toCardResponse(c srs.Card) cardResponse {
	return cardResponse{
		State: c.State, DueAt: c.Due, ScheduledDays: c.ScheduledDays, Reps: c.Reps, Lapses: c.Lapses,
		LastReviewAt: c.LastReview,
	}
}

// queueItem — карточка очереди: лицо (лемма) и оборот (перевод,
// дефиниции, примеры Tatoeba) в форме попапа слова.
type queueItem struct {
	TermID  int64  `json:"term_id"`
	Lemma   string `json:"lemma"`
	Surface string `json:"surface"`
	// Translation — перевод из кэша термина; null — слово не переводили
	// (очередь в LibreTranslate не ходит)
	Translation *string          `json:"translation"`
	Definitions []termDefinition `json:"definitions"`
	Examples    []termExample    `json:"examples"`
	Status      int              `json:"status"`
	Card        cardResponse     `json:"card"`
}

type queueResponse struct {
	Items []queueItem `json:"items"`
	// DueTotal — всех карточек к показу сейчас, не только в выдаче
	DueTotal int `json:"due_total"`
	// NextDueAt — ближайший показ после сейчас; null — будущих нет
	NextDueAt *time.Time `json:"next_due_at"`
}

// handlePracticeQueue — GET /practice/queue?limit= (S4-2): карточки
// вызывающего, чей показ наступил (due_at <= сейчас), самые просроченные
// первыми, не больше limit.
func (a *API) handlePracticeQueue(w http.ResponseWriter, r *http.Request) {
	limit := defaultQueueLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxQueueLimit {
			writeError(w, http.StatusBadRequest, "bad_request", "limit — от 1 до 50")
			return
		}
		limit = n
	}
	ctx := r.Context()
	userID := userFrom(ctx).ID
	now := a.now()
	cards, err := a.store.DueCards(ctx, userID, now, translationLang, limit)
	if err != nil {
		internalError(w, "due cards", err)
		return
	}
	resp := queueResponse{Items: make([]queueItem, 0, len(cards))}
	if resp.DueTotal, resp.NextDueAt, err = a.store.DueStats(ctx, userID, now); err != nil {
		internalError(w, "due stats", err)
		return
	}
	for _, c := range cards {
		defs, err := a.store.Definitions(ctx, []string{c.Lemma}, c.Surface, queueDefinitions)
		if err != nil {
			internalError(w, "definitions", err)
			return
		}
		item := queueItem{
			TermID: c.TermID, Lemma: c.Lemma, Surface: c.Surface, Translation: c.Translation,
			Definitions: make([]termDefinition, 0, len(defs)),
			Examples:    a.examples(ctx, &store.Term{ID: c.TermID, Lemma: c.Lemma}),
			Status:      c.Status, Card: toCardResponse(c.Card),
		}
		for _, d := range defs {
			item.Definitions = append(item.Definitions, termDefinition{
				Word: d.Word, Pos: d.Pos, Glosses: d.Glosses, TranslationsRU: d.TranslationsRU,
			})
		}
		resp.Items = append(resp.Items, item)
	}
	writeJSON(w, http.StatusOK, resp)
}

type reviewRequest struct {
	// ReviewID — uuid, который web выдумывает на показ карточки: повтор
	// запроса (сеть, двойное нажатие) засчитывается один раз
	ReviewID string `json:"review_id"`
	TermID   int64  `json:"term_id"`
	Rating   string `json:"rating"`
}

type reviewResponse struct {
	TermID int64      `json:"term_id"`
	Rating srs.Rating `json:"rating"`
	// Duplicate — оценку с этим review_id уже засчитали: график не тронут
	Duplicate bool `json:"duplicate"`
	// Status — статус слова в словаре после оценки (srs.Status)
	Status int          `json:"status"`
	Card   cardResponse `json:"card"`
}

// handleReview — POST /reviews {review_id, term_id, rating} (S4-3):
// оценка своей карточки → пересчёт FSRS → новое due_at и статус слова.
// Время оценки — часы api, клиент его не задаёт. 201 — засчитана,
// 200 — повтор того же review_id; 404 — карточки этого слова у
// вызывающего нет (чужие карточки этим путём недостижимы); 409
// not_due — показ ещё не наступил, review_conflict — review_id занят
// другой оценкой.
func (a *API) handleReview(w http.ResponseWriter, r *http.Request) {
	var req reviewRequest
	if err := decodeBody(w, r, &req); err != nil {
		badBody(w, err, "нужен JSON {review_id, term_id, rating}")
		return
	}
	rating, ok := srs.ParseRating(req.Rating)
	switch {
	case !validUUID(req.ReviewID):
		writeError(w, http.StatusBadRequest, "bad_request", "review_id — uuid вида xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx")
		return
	case req.TermID <= 0:
		writeError(w, http.StatusBadRequest, "bad_request", "некорректный term_id")
		return
	case !ok:
		writeError(w, http.StatusBadRequest, "bad_request", "rating — again, hard, good или easy")
		return
	}
	res, err := a.store.ReviewCard(r.Context(), a.srs, store.Review{
		UserID: userFrom(r.Context()).ID, TermID: req.TermID, ClientID: req.ReviewID,
		Rating: rating, Source: srs.SourceCard, At: a.now(),
	})
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "этого слова нет среди ваших карточек")
		return
	case errors.Is(err, store.ErrNotDue):
		writeError(w, http.StatusConflict, "not_due", "карточку уже повторили, следующий показ позже")
		return
	case errors.Is(err, store.ErrReviewConflict):
		writeError(w, http.StatusConflict, "review_conflict", "review_id уже занят другой оценкой")
		return
	case err != nil:
		internalError(w, "review card", err)
		return
	}
	status := http.StatusCreated
	if res.Duplicate {
		status = http.StatusOK
	}
	writeJSON(w, status, reviewResponse{
		TermID: req.TermID, Rating: rating, Duplicate: res.Duplicate, Status: res.Status, Card: toCardResponse(res.Card),
	})
}

// validUUID — uuid в каноническом виде 8-4-4-4-12 шестнадцатеричных
// цифр: в базе ключ хранится типом uuid, иной вид дал бы 500 вместо 400.
func validUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F') {
				return false
			}
		}
	}
	return true
}
