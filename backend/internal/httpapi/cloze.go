package httpapi

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/IEZhu/class/backend/internal/cloze"
	"github.com/IEZhu/class/backend/internal/lemma"
	"github.com/IEZhu/class/backend/internal/llm"
	"github.com/IEZhu/class/backend/internal/srs"
	"github.com/IEZhu/class/backend/internal/store"
	"github.com/IEZhu/class/backend/internal/tatoeba"
)

const (
	// Пунктов в выдаче GET /practice/cloze: по умолчанию и потолок —
	// вся выдача уходит одним вызовом LLM-дистракторов
	defaultClozeLimit = 10
	maxClozeLimit     = cloze.MaxLLMItems
	// clozeScan — во сколько раз больше слов просматривает выдача, чем
	// отдаёт пунктов: у слова может не найтись годного предложения
	clozeScan = 3
	// maxClozeResponseMS — потолок времени ответа: дольше — пункт
	// бросили и вернулись, «быстро» уже ни при чём
	maxClozeResponseMS = 10 * 60 * 1000
)

// Откуда варианты пункта (distractor_source).
const (
	distractorsList = "list"
	distractorsLLM  = "llm"
)

// clozeItemResponse — пункт без ответа: предложение вокруг пропуска
// и перемешанные варианты. Ни слова, ни термина, ни перевода
// предложения до ответа клиент не получает.
type clozeItemResponse struct {
	ID     int64  `json:"id"`
	Before string `json:"before"`
	After  string `json:"after"`
	// Options — ответ и дистракторы в нижнем регистре: из словаря —
	// словарные формы, от LLM — в форме пропуска
	Options          []string `json:"options"`
	DistractorSource string   `json:"distractor_source"`
	// Source — lesson | material | generated | tatoeba; LessonID — урок
	// реплики или материала
	Source   string `json:"source"`
	LessonID *int64 `json:"lesson_id"`
	// Attribution — подпись предложения Tatoeba (CC BY), null у остальных
	Attribution *exampleAttribution `json:"attribution"`
	// Due — показ карточки слова наступил: ответ сдвинет график; иначе
	// засчитается только промах (ADR-037)
	Due bool `json:"due"`
}

type clozeResponse struct {
	Items []clozeItemResponse `json:"items"`
	// DueTotal — карточек к показу сейчас всего
	DueTotal int `json:"due_total"`
	// LLMAvailable — можно просить LLM-дистракторы (POST
	// /practice/cloze/distractors)
	LLMAvailable bool `json:"llm_available"`
}

// clozeOptions — варианты пункта и их источник. LLM-дистракторы подобраны
// в форме пропуска, поэтому ответ среди них — форма из предложения;
// дистракторы словаря — леммы, и ответ — лемма: иначе одна форма среди
// словарных выдала бы ответ.
func clozeOptions(it *store.ClozeItem) ([]string, string) {
	if it.LLM != nil {
		_, surface, _ := cloze.Parts(it.Sentence, it.Blank)
		return cloze.Options(surface, it.LLM, it.ContextHash), distractorsLLM
	}
	return cloze.Options(it.Lemma, it.Distractors, it.ContextHash), distractorsList
}

func toClozeItemResponse(it *store.ClozeItem) clozeItemResponse {
	before, _, after := cloze.Parts(it.Sentence, it.Blank)
	options, source := clozeOptions(it)
	resp := clozeItemResponse{
		ID: it.ID, Before: before, After: after, Options: options, DistractorSource: source,
		Source: it.Source, LessonID: it.LessonID, Due: it.Due,
	}
	if e := it.Example; e != nil {
		resp.Attribution = &exampleAttribution{
			Author: e.Author, URL: tatoeba.SentenceURL(e.ID),
			TranslationAuthor: e.TranslationAuthor, TranslationURL: tatoeba.SentenceURL(e.TranslationID),
			License: tatoeba.License, LicenseURL: tatoeba.LicenseURL,
		}
	}
	return resp
}

// handlePracticeCloze — GET /practice/cloze?limit= (S4-4, S4-5): пункты
// по словам вызывающего — сначала те, чей показ наступил, затем шаги
// обучения, затем остальные. Варианты — из кэша LLM-дистракторов, если
// они там есть, иначе из словаря; без ключа LLM выдача та же.
func (a *API) handlePracticeCloze(w http.ResponseWriter, r *http.Request) {
	limit := defaultClozeLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxClozeLimit {
			writeError(w, http.StatusBadRequest, "bad_request", "limit — от 1 до 20")
			return
		}
		limit = n
	}
	ctx := r.Context()
	userID := userFrom(ctx).ID
	now := a.now()
	targets, err := a.store.ClozeTargets(ctx, userID, now, limit*clozeScan)
	if err != nil {
		internalError(w, "cloze targets", err)
		return
	}
	ids, err := a.clozeItemIDs(ctx, userID, targets, limit)
	if err != nil {
		internalError(w, "cloze items", err)
		return
	}
	items, err := a.store.ClozeItems(ctx, userID, ids, now)
	if err != nil {
		internalError(w, "cloze items", err)
		return
	}
	resp := clozeResponse{Items: make([]clozeItemResponse, 0, len(items)), LLMAvailable: a.llm != nil}
	if resp.DueTotal, _, err = a.store.DueStats(ctx, userID, now); err != nil {
		internalError(w, "due stats", err)
		return
	}
	for i := range items {
		resp.Items = append(resp.Items, toClozeItemResponse(&items[i]))
	}
	writeJSON(w, http.StatusOK, resp)
}

// clozeItemIDs — пункты выдачи по целям в их порядке, не больше limit.
// У слова берётся пункт, на который ещё не отвечали; нет такого — новый
// из первого незанятого предложения его текстов; нет и его — пункт,
// на который отвечали давнее всех. Слово без годного предложения
// пропускается. Тексты уроков ищутся одной пачкой на все слова без
// свободного пункта, тексты вне уроков — по слову, когда в уроках
// предложение не нашлось.
func (a *API) clozeItemIDs(ctx context.Context, userID int64, targets []store.ClozeTarget, limit int) ([]int64, error) {
	termIDs := make([]int64, 0, len(targets))
	for _, t := range targets {
		termIDs = append(termIDs, t.TermID)
	}
	refs, err := a.store.ClozeItemRefs(ctx, userID, termIDs)
	if err != nil {
		return nil, err
	}
	byTerm := map[int64][]store.ClozeItemRef{}
	for _, ref := range refs {
		byTerm[ref.TermID] = append(byTerm[ref.TermID], ref)
	}
	unanswered := func(t store.ClozeTarget) int {
		return slices.IndexFunc(byTerm[t.TermID], func(r store.ClozeItemRef) bool { return r.Attempts == 0 })
	}
	var queries []store.ClozeQuery
	query := map[int64]int{} // слово → номер его запроса в queries
	for _, t := range targets {
		if unanswered(t) < 0 {
			query[t.TermID] = len(queries)
			queries = append(queries, store.ClozeQuery{Words: append([]string{t.Lemma}, lemma.Forms(t.Lemma)...), LessonID: t.LessonID})
		}
	}
	lessonTexts, err := a.store.ClozeLessonTexts(ctx, userID, queries)
	if err != nil {
		return nil, err
	}
	var words []cloze.Word
	if len(queries) > 0 {
		if words, err = a.store.ClozeWords(ctx, userID); err != nil {
			return nil, err
		}
	}
	out := make([]int64, 0, limit)
	for _, t := range targets {
		if len(out) == limit {
			break
		}
		own := byTerm[t.TermID]
		if i := unanswered(t); i >= 0 {
			out = append(out, own[i].ID)
			continue
		}
		id, ok, err := a.newClozeItem(ctx, userID, t, own, words, lessonTexts[query[t.TermID]])
		switch {
		case err != nil:
			return nil, err
		case ok:
			out = append(out, id)
		case len(own) > 0:
			oldest := slices.MinFunc(own, func(x, y store.ClozeItemRef) int { return x.LastAttemptAt.Compare(*y.LastAttemptAt) })
			out = append(out, oldest.ID)
		}
	}
	return out, nil
}

// newClozeItem — новый пункт слова из первого предложения его текстов
// (уроков, затем вне уроков), которое годится (cloze.Find) и ещё не
// стало пунктом. false — такого предложения нет или слово убрали из
// словаря.
func (a *API) newClozeItem(ctx context.Context, userID int64, t store.ClozeTarget, own []store.ClozeItemRef,
	words []cloze.Word, lessonTexts []store.ClozeText) (int64, bool, error) {
	used := make(map[string]bool, len(own))
	for _, ref := range own {
		used[string(ref.ContextHash)] = true
	}
	try := func(texts []store.ClozeText) (int64, bool, error) {
		for _, text := range texts {
			for _, sentence := range cloze.Sentences(text.Text) {
				b, ok := cloze.Find(sentence, t.Lemma)
				if !ok {
					continue
				}
				hash := cloze.Hash(sentence, b)
				if used[string(hash)] {
					continue
				}
				_, surface, _ := cloze.Parts(sentence, b)
				distractors := cloze.PickDistractors(cloze.Target{Lemma: t.Lemma, Surface: surface, LessonID: t.LessonID},
					words, sentence, hash)
				if len(distractors) < cloze.Distractors {
					continue
				}
				id, err := a.store.CreateClozeItem(ctx, &store.ClozeItem{
					UserID: userID, TermID: t.TermID, ClozeRef: text.ClozeRef,
					Sentence: sentence, Blank: b, ContextHash: hash, Distractors: distractors,
				})
				if errors.Is(err, store.ErrNotFound) {
					return 0, false, nil
				}
				return id, err == nil, err
			}
		}
		return 0, false, nil
	}
	if id, ok, err := try(lessonTexts); ok || err != nil {
		return id, ok, err
	}
	other, err := a.store.ClozeOtherTexts(ctx, userID, t.Lemma)
	if err != nil {
		return 0, false, err
	}
	return try(other)
}

type clozeAttemptRequest struct {
	// AttemptID — uuid, который web выдумывает на показ пункта: повтор
	// запроса засчитывается один раз
	AttemptID string `json:"attempt_id"`
	Answer    string `json:"answer"`
	Mode      string `json:"mode"`
	// ResponseMS — от показа пункта до ответа, по часам браузера
	ResponseMS *int `json:"response_ms"`
}

type clozeAttemptResponse struct {
	ItemID int64  `json:"item_id"`
	TermID int64  `json:"term_id"`
	Lemma  string `json:"lemma"`
	// Answer — форма слова в предложении, Sentence — предложение целиком
	Answer   string `json:"answer"`
	Sentence string `json:"sentence"`
	Correct  bool   `json:"correct"`
	// Exact — ответ — именно форма из предложения, а не другая форма слова
	Exact  bool       `json:"exact"`
	Rating srs.Rating `json:"rating"`
	// Counted — ответ ушёл в график FSRS (srs_reviews, source cloze)
	Counted bool `json:"counted"`
	// Duplicate — попытку с этим attempt_id уже засчитали
	Duplicate bool         `json:"duplicate"`
	Status    int          `json:"status"`
	Card      cardResponse `json:"card"`
	// Translation — перевод предложения Tatoeba; null у остальных
	Translation *string `json:"translation"`
}

// handleClozeAttempt — POST /practice/cloze/{id}/attempts {attempt_id,
// answer, mode, response_ms} (S4-5): ответ на свой пункт → оценка FSRS
// (верно и быстро — easy, верно — good, неверно — again) → карточка
// и статус слова. Засчитывается ответ по карточке к показу и промах по
// любой; верный ответ раньше показа только пишется в журнал (ADR-037).
// 201 — записан, 200 — повтор того же attempt_id; 404 — пункта нет
// у вызывающего (чужие недостижимы) или слово убрали; 409
// attempt_conflict — attempt_id занят другим ответом.
func (a *API) handleClozeAttempt(w http.ResponseWriter, r *http.Request) {
	itemID, ok := pathID(w, r)
	if !ok {
		return
	}
	var req clozeAttemptRequest
	if err := decodeBody(w, r, &req); err != nil {
		badBody(w, err, "нужен JSON {attempt_id, answer, mode, response_ms}")
		return
	}
	answer := strings.TrimSpace(req.Answer)
	mode, modeOK := cloze.ParseMode(req.Mode)
	switch {
	case !validUUID(req.AttemptID):
		writeError(w, http.StatusBadRequest, "bad_request", "attempt_id — uuid вида xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx")
		return
	case answer == "" || len(answer) > lemma.MaxWordBytes:
		writeError(w, http.StatusBadRequest, "bad_request", "answer — непустое слово до 64 байт")
		return
	case !modeOK:
		writeError(w, http.StatusBadRequest, "bad_request", "mode — choice или typing")
		return
	case req.ResponseMS == nil || *req.ResponseMS < 0 || *req.ResponseMS > maxClozeResponseMS:
		writeError(w, http.StatusBadRequest, "bad_request", "response_ms — от 0 до 600000")
		return
	}
	ctx := r.Context()
	userID := userFrom(ctx).ID
	now := a.now()
	items, err := a.store.ClozeItems(ctx, userID, []int64{itemID}, now)
	if err != nil {
		internalError(w, "cloze item", err)
		return
	}
	if len(items) == 0 {
		writeError(w, http.StatusNotFound, "not_found", "пункт не найден")
		return
	}
	it := &items[0]
	_, surface, _ := cloze.Parts(it.Sentence, it.Blank)
	correct, exact := cloze.Check(answer, surface, it.Lemma)
	res, err := a.store.RecordClozeAttempt(ctx, a.srs, store.ClozeAttempt{
		UserID: userID, ItemID: it.ID, ClientID: req.AttemptID, Mode: mode, Answer: answer,
		Correct: correct, ResponseMS: *req.ResponseMS, Rating: cloze.Rate(correct, mode, *req.ResponseMS), At: now,
	})
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "пункт не найден: слова больше нет в словаре")
		return
	case errors.Is(err, store.ErrAttemptConflict):
		writeError(w, http.StatusConflict, "attempt_conflict", "attempt_id уже занят другим ответом")
		return
	case err != nil:
		internalError(w, "cloze attempt", err)
		return
	}
	resp := clozeAttemptResponse{
		ItemID: it.ID, TermID: res.TermID, Lemma: it.Lemma, Answer: surface, Sentence: it.Sentence,
		Correct: res.Correct, Exact: exact, Rating: res.Rating, Counted: res.Counted,
		Duplicate: res.Duplicate, Status: res.Status, Card: toCardResponse(res.Card),
	}
	if it.Example != nil {
		resp.Translation = &it.Example.Translation
	}
	status := http.StatusCreated
	if res.Duplicate {
		status = http.StatusOK
	}
	writeJSON(w, status, resp)
}

type clozeDistractorsRequest struct {
	ItemIDs []int64 `json:"item_ids"`
}

type clozeOptionsResponse struct {
	ID               int64    `json:"id"`
	Options          []string `json:"options"`
	DistractorSource string   `json:"distractor_source"`
}

type clozeDistractorsResponse struct {
	Items []clozeOptionsResponse `json:"items"`
}

// handleClozeDistractors — POST /practice/cloze/distractors {item_ids}
// (S5-8, часть 2): LLM подбирает правдоподобные неверные варианты к своим
// пунктам выдачи — один вызов на все пункты без кэша. Варианты проверяет
// cloze.ParseDistractors; пункт с негодным ответом остаётся с
// дистракторами словаря. Годные кладутся в общий кэш по слову и
// предложению: повтор провайдера не зовёт и лимит не тратит. Лимит —
// суточный счётчик «объясни» (тот же вид дешёвых словесных вызовов,
// ADR-037); без ключа — 503, как у генерации и «объясни», а сам cloze
// работает на дистракторах словаря.
func (a *API) handleClozeDistractors(w http.ResponseWriter, r *http.Request) {
	if a.llm == nil {
		writeError(w, http.StatusServiceUnavailable, "llm_not_configured", "LLM не настроен")
		return
	}
	var req clozeDistractorsRequest
	if err := decodeBody(w, r, &req); err != nil {
		badBody(w, err, "нужен JSON {item_ids}")
		return
	}
	if len(req.ItemIDs) == 0 || len(req.ItemIDs) > cloze.MaxLLMItems {
		writeError(w, http.StatusBadRequest, "bad_request", "item_ids — от 1 до 20 пунктов")
		return
	}
	seen := map[int64]bool{}
	for _, id := range req.ItemIDs {
		if id <= 0 || seen[id] {
			writeError(w, http.StatusBadRequest, "bad_request", "item_ids — разные положительные id")
			return
		}
		seen[id] = true
	}
	ctx := r.Context()
	u := userFrom(ctx)
	items, err := a.store.ClozeItems(ctx, u.ID, req.ItemIDs, a.now())
	if err != nil {
		internalError(w, "cloze items", err)
		return
	}
	if len(items) != len(req.ItemIDs) {
		writeError(w, http.StatusNotFound, "not_found", "пункт не найден")
		return
	}
	var need []int // номера пунктов без LLM-дистракторов в кэше
	var batch []cloze.LLMItem
	for i := range items {
		if items[i].LLM == nil {
			need = append(need, i)
			batch = append(batch, cloze.NewLLMItem(strconv.Itoa(len(batch)+1), items[i].Sentence, items[i].Blank, items[i].Lemma))
		}
	}
	if len(batch) > 0 {
		ok, err := a.store.ReserveLLMRequest(ctx, u.ID, store.LLMKindExplain, a.llmExplainDailyLimit)
		if err != nil {
			internalError(w, "reserve llm request", err)
			return
		}
		if !ok {
			writeError(w, http.StatusTooManyRequests, "rate_limited", "лимит подсказок LLM на сегодня исчерпан")
			return
		}
		// Вызов может длиться дольше WriteTimeout сервера (20 с), как
		// у «объясни»
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(explainBudget + 5*time.Second))
		lctx, cancel := context.WithTimeout(ctx, explainBudget)
		defer cancel()
		got, model, err := cloze.Generate(lctx, a.llm, batch)
		switch {
		case llm.Temporary(err):
			logf("cloze distractors user %d: %v", u.ID, err)
			writeError(w, http.StatusServiceUnavailable, "llm_unavailable", "LLM временно недоступен, попробуйте позже")
			return
		case err != nil:
			logf("cloze distractors user %d: %v", u.ID, err)
			writeError(w, http.StatusBadGateway, "llm_failed", "LLM не справился с запросом")
			return
		}
		for k, i := range need {
			opts, ok := got[batch[k].ID]
			if !ok {
				continue
			}
			// Оплаченный ответ сохраняем, даже если клиент уже ушёл
			if err := a.store.SaveClozeDistractors(context.WithoutCancel(ctx), items[i].TermID, items[i].ContextHash,
				opts, a.llm.Name(), model); err != nil {
				internalError(w, "save cloze distractors", err)
				return
			}
			items[i].LLM = opts
		}
		if rejected := len(batch) - len(got); rejected > 0 {
			logf("cloze distractors user %d: ответ LLM не прошёл проверку у %d из %d пунктов", u.ID, rejected, len(batch))
		}
	}
	resp := clozeDistractorsResponse{Items: make([]clozeOptionsResponse, 0, len(items))}
	for i := range items {
		options, source := clozeOptions(&items[i])
		resp.Items = append(resp.Items, clozeOptionsResponse{ID: items[i].ID, Options: options, DistractorSource: source})
	}
	writeJSON(w, http.StatusOK, resp)
}
