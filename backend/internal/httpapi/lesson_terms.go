package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/IEZhu/class/backend/internal/lemma"
	"github.com/IEZhu/class/backend/internal/lessonterms"
	"github.com/IEZhu/class/backend/internal/store"
)

// Словарь урока (S3-7, S3-8): кандидаты из кликов, доски и ручного ввода
// (ADR-033), чеклист преподавателя → lesson_terms → назначение студентам
// в user_terms (ADR-034).

// maxManualWords — слов в одном ручном вводе: чеклист урока, а не импорт
// словаря.
const maxManualWords = 100

type candidateResponse struct {
	TermID  int64  `json:"term_id"`
	Lemma   string `json:"lemma"`
	Surface string `json:"surface"`
	// Translation — перевод из кэша термина; null — слово не переводили
	Translation *string   `json:"translation"`
	Source      string    `json:"source"`
	Count       int       `json:"count"`
	FirstSeenAt time.Time `json:"first_seen_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
	// Confirmed — термин уже в словаре урока
	Confirmed bool `json:"confirmed"`
}

type candidatesResponse struct {
	Items []candidateResponse `json:"items"`
}

type manualCandidatesRequest struct {
	Words []string `json:"words"`
}

type addedCandidate struct {
	TermID  int64  `json:"term_id"`
	Lemma   string `json:"lemma"`
	Surface string `json:"surface"`
}

// rejectedWord — слово ручного ввода, которое не стало кандидатом:
// not_a_word — не одно слово из букв, unknown — его нет ни в OEWN,
// ни в словарном датасете, термин не заводится (ADR-017).
type rejectedWord struct {
	Word   string `json:"word"`
	Reason string `json:"reason"`
}

type manualCandidatesResponse struct {
	Added    []addedCandidate `json:"added"`
	Rejected []rejectedWord   `json:"rejected"`
}

type whiteboardCandidatesResponse struct {
	// Candidates — кандидатов с доски после разбора; доски нет — 0
	Candidates int `json:"candidates"`
}

type lessonTermResponse struct {
	TermID      int64     `json:"term_id"`
	Lemma       string    `json:"lemma"`
	Surface     string    `json:"surface"`
	Translation *string   `json:"translation"`
	ConfirmedAt time.Time `json:"confirmed_at"`
}

type lessonTermsResponse struct {
	Items []lessonTermResponse `json:"items"`
}

type setLessonTermsRequest struct {
	TermIDs []int64 `json:"term_ids"`
}

type assignLessonTermsRequest struct {
	// All — всем студентам урока; иначе — только StudentIDs
	All        bool    `json:"all"`
	StudentIDs []int64 `json:"student_ids"`
}

type assignLessonTermsResponse struct {
	Students int `json:"students"`
	Terms    int `json:"terms"`
	// Added — слов легло в словари впервые, Kept — уже были (статус
	// не тронут)
	Added int `json:"added"`
	Kept  int `json:"kept"`
}

// lessonOwner — урок из пути, если вошедший — его преподаватель: урока
// нет — 404, чужой — 403. false — ответ уже записан.
func (a *API) lessonOwner(w http.ResponseWriter, r *http.Request) (*store.LessonDetail, bool) {
	lessonID, ok := pathID(w, r)
	if !ok {
		return nil, false
	}
	d, err := a.store.GetLessonDetail(r.Context(), lessonID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "урок не найден")
		return nil, false
	case err != nil:
		internalError(w, "get lesson", err)
		return nil, false
	case userFrom(r.Context()).ID != d.TeacherID:
		writeError(w, http.StatusForbidden, "forbidden", "словарь урока собирает teacher этого урока")
		return nil, false
	}
	return d, true
}

// handleListCandidates — GET /lessons/{id}/term-candidates: чеклист
// кандидатов всех источников.
func (a *API) handleListCandidates(w http.ResponseWriter, r *http.Request) {
	d, ok := a.lessonOwner(w, r)
	if !ok {
		return
	}
	a.writeCandidates(w, r, d.ID)
}

func (a *API) writeCandidates(w http.ResponseWriter, r *http.Request, lessonID int64) {
	cs, err := a.store.LessonCandidates(r.Context(), lessonID, translationLang)
	if err != nil {
		internalError(w, "lesson candidates", err)
		return
	}
	resp := candidatesResponse{Items: make([]candidateResponse, 0, len(cs))}
	for _, c := range cs {
		resp.Items = append(resp.Items, candidateResponse{
			TermID: c.TermID, Lemma: c.Lemma, Surface: c.Surface, Translation: c.Translation,
			Source: c.Source, Count: c.Count, FirstSeenAt: c.FirstSeenAt, LastSeenAt: c.LastSeenAt,
			Confirmed: c.Confirmed,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleAddManualCandidates — POST /lessons/{id}/term-candidates: слова,
// которые преподаватель вводит сам. Каждое — одно слово; не слово
// и незнакомое возвращаются в rejected, остальные становятся кандидатами
// source='manual' по правилу клика (лемма, термин только у знакомого).
func (a *API) handleAddManualCandidates(w http.ResponseWriter, r *http.Request) {
	d, ok := a.lessonOwner(w, r)
	if !ok {
		return
	}
	var req manualCandidatesRequest
	if err := decodeBody(w, r, &req); err != nil {
		badBody(w, err, "некорректное тело запроса")
		return
	}
	if len(req.Words) == 0 || len(req.Words) > maxManualWords {
		writeError(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("words — от 1 до %d слов", maxManualWords))
		return
	}
	resp := manualCandidatesResponse{Added: []addedCandidate{}, Rejected: []rejectedWord{}}
	var words []lessonterms.Word
	for _, raw := range req.Words {
		surface := strings.TrimSpace(raw)
		if !lemma.IsWord(surface) || lemma.Normalize(surface) == "" {
			resp.Rejected = append(resp.Rejected, rejectedWord{Word: surface, Reason: "not_a_word"})
			continue
		}
		lem := lemma.Lemma(surface)
		if !slices.ContainsFunc(words, func(w lessonterms.Word) bool { return w.Lemma == lem }) {
			words = append(words, lessonterms.Word{Lemma: lem, Surface: surface, Count: 1})
		}
	}
	ctx := r.Context()
	known, unknown, err := lessonterms.Known(ctx, a.store, words)
	if err != nil {
		internalError(w, "known words", err)
		return
	}
	for _, u := range unknown {
		resp.Rejected = append(resp.Rejected, rejectedWord{Word: u.Surface, Reason: "unknown"})
	}
	terms, err := lessonterms.Terms(ctx, a.store, known)
	if err != nil {
		internalError(w, "ensure terms", err)
		return
	}
	if err := a.store.AddManualCandidates(ctx, d.ID, d.TeacherID, terms); err != nil {
		internalError(w, "manual candidates", err)
		return
	}
	for i, t := range terms {
		resp.Added = append(resp.Added, addedCandidate{TermID: t.TermID, Lemma: known[i].Lemma, Surface: t.Surface})
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleCollectWhiteboard — POST /lessons/{id}/term-candidates/whiteboard:
// разобрать доску сейчас, не дожидаясь конца урока (урок без LiveKit
// в live не переходит, и джоба конца урока у него не ставится).
func (a *API) handleCollectWhiteboard(w http.ResponseWriter, r *http.Request) {
	d, ok := a.lessonOwner(w, r)
	if !ok {
		return
	}
	n, err := lessonterms.CollectWhiteboard(r.Context(), a.store, d.ID)
	if err != nil {
		internalError(w, "collect whiteboard", err)
		return
	}
	writeJSON(w, http.StatusOK, whiteboardCandidatesResponse{Candidates: n})
}

// handleGetLessonTerms — GET /lessons/{id}/terms: словарь урока видят
// все его участники.
func (a *API) handleGetLessonTerms(w http.ResponseWriter, r *http.Request) {
	lessonID, ok := pathID(w, r)
	if !ok {
		return
	}
	if _, ok := a.visibleLesson(r.Context(), w, userFrom(r.Context()), lessonID); !ok {
		return
	}
	a.writeLessonTerms(w, r, lessonID)
}

func (a *API) writeLessonTerms(w http.ResponseWriter, r *http.Request, lessonID int64) {
	ts, err := a.store.LessonTerms(r.Context(), lessonID, translationLang)
	if err != nil {
		internalError(w, "lesson terms", err)
		return
	}
	resp := lessonTermsResponse{Items: make([]lessonTermResponse, 0, len(ts))}
	for _, t := range ts {
		resp.Items = append(resp.Items, lessonTermResponse{
			TermID: t.TermID, Lemma: t.Lemma, Surface: t.Surface, Translation: t.Translation, ConfirmedAt: t.ConfirmedAt,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleSetLessonTerms — PUT /lessons/{id}/terms: подтверждение чеклиста.
// Список — словарь урока целиком: снятые термины уходят из него, но не из
// словарей студентов, которым их уже назначили.
func (a *API) handleSetLessonTerms(w http.ResponseWriter, r *http.Request) {
	d, ok := a.lessonOwner(w, r)
	if !ok {
		return
	}
	var req setLessonTermsRequest
	if err := decodeBody(w, r, &req); err != nil || req.TermIDs == nil {
		badBody(w, err, "нужен term_ids — список id терминов из кандидатов урока")
		return
	}
	notCandidates, err := a.store.SetLessonTerms(r.Context(), d.ID, d.TeacherID, req.TermIDs)
	if err != nil {
		internalError(w, "set lesson terms", err)
		return
	}
	if len(notCandidates) > 0 {
		writeError(w, http.StatusBadRequest, "not_candidates",
			fmt.Sprintf("в словарь урока — только его кандидаты; не кандидаты: %v", notCandidates))
		return
	}
	a.writeLessonTerms(w, r, d.ID)
}

// handleAssignLessonTerms — POST /lessons/{id}/terms/assignments: словарь
// урока — в личные словари всех или выбранных студентов урока (ADR-034).
// Адресаты — студенты из снапшота участников, не в архиве: гостю события
// и преподавателю личный словарь не положен (ADR-027).
func (a *API) handleAssignLessonTerms(w http.ResponseWriter, r *http.Request) {
	d, ok := a.lessonOwner(w, r)
	if !ok {
		return
	}
	var req assignLessonTermsRequest
	if err := decodeBody(w, r, &req); err != nil {
		badBody(w, err, "некорректное тело запроса")
		return
	}
	if req.All == (len(req.StudentIDs) > 0) {
		writeError(w, http.StatusBadRequest, "bad_request", "нужно одно из двух: all: true или непустой student_ids")
		return
	}
	var students []int64
	for _, p := range d.Participants {
		if p.Role == store.RoleStudent && !p.Archived {
			students = append(students, p.UserID)
		}
	}
	if !req.All {
		picked := slices.Clone(req.StudentIDs)
		slices.Sort(picked)
		picked = slices.Compact(picked)
		for _, id := range picked {
			if !slices.Contains(students, id) {
				writeError(w, http.StatusBadRequest, "not_student",
					fmt.Sprintf("пользователь %d — не студент этого урока", id))
				return
			}
		}
		students = picked
	}
	if len(students) == 0 {
		writeError(w, http.StatusConflict, "no_students", "у урока нет студентов")
		return
	}
	res, err := a.store.AssignLessonTerms(r.Context(), d.ID, students)
	if err != nil {
		internalError(w, "assign lesson terms", err)
		return
	}
	if res.Terms == 0 {
		writeError(w, http.StatusConflict, "no_lesson_terms", "словарь урока пуст: сначала подтвердите слова")
		return
	}
	writeJSON(w, http.StatusOK, assignLessonTermsResponse{
		Students: res.Students, Terms: res.Terms, Added: res.Added, Kept: res.Kept,
	})
}
