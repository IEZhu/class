package httpapi

import (
	"context"
	"errors"
	"net/http"
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
	// clozeScan — страница очереди слов: во столько раз больше слов, чем
	// пунктов в выдаче, — у слова может не найтись годного предложения
	clozeScan = 3
	// clozeMaxTargets — потолок слов, которые одна выдача просматривает
	// страницами, пока не наберёт limit пунктов
	clozeMaxTargets = 200
	// clozeMissTTL — сколько слово без нового предложения не ищется
	// заново, если новых источников не появилось (ADR-037)
	clozeMissTTL = time.Hour
	// maxClozeResponseMS — потолок времени ответа: дольше — пункт
	// бросили и вернулись, «быстро» уже ни при чём
	maxClozeResponseMS = 10 * 60 * 1000
)

// Откуда варианты пункта (distractor_source).
const (
	distractorsList = "list"
	distractorsLLM  = "llm"
	// distractorsPending — вариантов ещё нет: их отдаст POST
	// /practice/cloze/distractors (LLM или, при сбое, словарь)
	distractorsPending = "pending"
)

// clozeAttribution — подпись предложения Tatoeba до ответа: автор и
// лицензия (CC BY), без ссылок на страницы предложения и перевода — там
// предложение целиком, с ответом. Ссылки — в ответе на попытку.
type clozeAttribution struct {
	Author     string `json:"author"`
	License    string `json:"license"`
	LicenseURL string `json:"license_url"`
}

// clozeItemResponse — пункт без ответа: предложение вокруг пропуска
// и перемешанные варианты. Ни слова, ни термина, ни перевода
// предложения до ответа клиент не получает.
type clozeItemResponse struct {
	ID     int64  `json:"id"`
	Before string `json:"before"`
	After  string `json:"after"`
	// Options — ответ и дистракторы в нижнем регистре: из словаря —
	// словарные формы, от LLM — в форме пропуска; пусто у pending. У
	// пункта один набор за всё время (cloze_items.option_source)
	Options          []string `json:"options"`
	DistractorSource string   `json:"distractor_source"`
	// Source — lesson | material | generated | tatoeba; LessonID — урок
	// реплики или материала
	Source   string `json:"source"`
	LessonID *int64 `json:"lesson_id"`
	// Attribution — автор и лицензия предложения Tatoeba, null у остальных
	Attribution *clozeAttribution `json:"attribution"`
	// Due — показ карточки слова наступил: ответ сдвинет график; иначе
	// засчитается только промах (ADR-037)
	Due bool `json:"due"`
}

type clozeResponse struct {
	Items []clozeItemResponse `json:"items"`
	// DueTotal — карточек к показу сейчас всего
	DueTotal int `json:"due_total"`
	// LLMAvailable — можно просить LLM-дистракторы (POST
	// /practice/cloze/distractors); без него pending не бывает
	LLMAvailable bool `json:"llm_available"`
}

// clozeOptions — варианты пункта по закреплённому набору. LLM-дистракторы
// подобраны в форме пропуска, поэтому ответ среди них — форма из
// предложения; дистракторы словаря — леммы, и ответ — лемма: иначе одна
// форма среди словарных выдала бы ответ. Набор не закреплён — вариантов
// нет (pending).
func clozeOptions(it *store.ClozeItem) ([]string, string) {
	switch it.OptionSource {
	case store.ClozeOptionsLLM:
		_, surface, _ := cloze.Parts(it.Sentence, it.Blank)
		return cloze.Options(surface, it.LLM, it.ContextHash), distractorsLLM
	case store.ClozeOptionsList:
		return cloze.Options(it.Lemma, it.Distractors, it.ContextHash), distractorsList
	}
	return []string{}, distractorsPending
}

func toClozeItemResponse(it *store.ClozeItem) clozeItemResponse {
	before, _, after := cloze.Parts(it.Sentence, it.Blank)
	options, source := clozeOptions(it)
	resp := clozeItemResponse{
		ID: it.ID, Before: before, After: after, Options: options, DistractorSource: source,
		Source: it.Source, LessonID: it.LessonID, Due: it.Due,
	}
	if e := it.Example; e != nil {
		resp.Attribution = &clozeAttribution{Author: e.Author, License: tatoeba.License, LicenseURL: tatoeba.LicenseURL}
	}
	return resp
}

// handlePracticeCloze — GET /practice/cloze?limit= (S4-4, S4-5): пункты
// по словам вызывающего — сначала те, чей показ наступил, затем шаги
// обучения, затем остальные. Набор вариантов пункт получает один раз
// (ADR-037): из кэша LLM-дистракторов, если запись там есть; иначе
// дистракторы словаря — без ключа LLM у всех пунктов, с ключом у первого
// (он сразу на экране); остальным с ключом варианты отдаст POST
// /practice/cloze/distractors (pending).
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
	ids, err := a.clozeItemIDs(ctx, userID, now, limit)
	if err != nil {
		internalError(w, "cloze items", err)
		return
	}
	items, err := a.store.ClozeItems(ctx, userID, ids, now)
	if err != nil {
		internalError(w, "cloze items", err)
		return
	}
	var list, fromCache []int64
	for i := range items {
		switch it := &items[i]; {
		case it.OptionSource != "":
		case it.LLM != nil:
			fromCache = append(fromCache, it.ID)
		case a.llm == nil || i == 0:
			list = append(list, it.ID)
		}
	}
	if items, err = a.lockClozeOptions(ctx, userID, items, list, fromCache, now); err != nil {
		internalError(w, "cloze options", err)
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

// lockClozeOptions закрепляет наборы вариантов за пунктами и читает
// пункты заново: какой набор достался пункту, решает база (первая
// отдача, в том числе параллельная).
func (a *API) lockClozeOptions(ctx context.Context, userID int64, items []store.ClozeItem, list, fromCache []int64,
	now time.Time) ([]store.ClozeItem, error) {
	if len(list) == 0 && len(fromCache) == 0 {
		return items, nil
	}
	if err := a.store.LockClozeOptions(ctx, userID, list, fromCache); err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	return a.store.ClozeItems(ctx, userID, ids, now)
}

// clozeBatch — набираемая выдача. Предложение — не больше чем в одном
// пункте: итог ответа показывает его целиком, и второй пункт из того же
// предложения решался бы по памяти (ADR-037).
type clozeBatch struct {
	ids       []int64
	sentences map[string]bool
	limit     int
	// words — словарь пользователя (запас дистракторов): читается один
	// раз, когда понадобился новый пункт
	words       []cloze.Word
	wordsLoaded bool
}

func (b *clozeBatch) full() bool { return len(b.ids) >= b.limit }

func (b *clozeBatch) add(id int64, sentence string) {
	b.ids = append(b.ids, id)
	b.sentences[sentence] = true
}

// pick — пункт слова для выдачи, чьё предложение ещё не в ней: первый
// неотвеченный (answered false) или, из отвеченных, тот, на который
// отвечали давнее всех.
func (b *clozeBatch) pick(own []store.ClozeItemRef, answered bool) (store.ClozeItemRef, bool) {
	var best store.ClozeItemRef
	found := false
	for _, ref := range own {
		if b.sentences[ref.Sentence] || (ref.Attempts > 0) != answered {
			continue
		}
		if !answered {
			return ref, true
		}
		if !found || ref.LastAttemptAt.Before(*best.LastAttemptAt) {
			best, found = ref, true
		}
	}
	return best, found
}

// clozeItemIDs — пункты выдачи, не больше limit. Слова идут страницами
// очереди (ClozeTargets по limit*clozeScan), пока выдача не наберётся
// или не кончатся clozeMaxTargets слов: слово без годного предложения не
// занимает места у слов с предложениями.
func (a *API) clozeItemIDs(ctx context.Context, userID int64, now time.Time, limit int) ([]int64, error) {
	b := &clozeBatch{limit: limit, sentences: map[string]bool{}}
	page := limit * clozeScan
	for offset := 0; !b.full() && offset < clozeMaxTargets; offset += page {
		n := min(page, clozeMaxTargets-offset)
		targets, err := a.store.ClozeTargets(ctx, userID, now, now.Add(-clozeMissTTL), n, offset)
		if err != nil {
			return nil, err
		}
		if err := a.clozeFill(ctx, userID, now, b, targets); err != nil {
			return nil, err
		}
		if len(targets) < n {
			break
		}
	}
	return b.ids, nil
}

// clozeFill добирает выдачу из страницы слов в их порядке. У слова
// берётся неотвеченный пункт; нет такого — новый из первого незанятого
// предложения его текстов; нет и его — пункт, на который отвечали давнее
// всех. Предложение уже в выдаче — слово ждёт следующей. Слово, которому
// нового предложения не нашлось, запоминается в cloze_misses и до
// устаревания записи не ищется (Checked). Тексты уроков ищутся одной
// пачкой на все слова страницы, которым нужен поиск, тексты вне уроков —
// по слову, когда в уроках предложение не нашлось.
func (a *API) clozeFill(ctx context.Context, userID int64, now time.Time, b *clozeBatch, targets []store.ClozeTarget) error {
	termIDs := make([]int64, 0, len(targets))
	for _, t := range targets {
		termIDs = append(termIDs, t.TermID)
	}
	refs, err := a.store.ClozeItemRefs(ctx, userID, termIDs)
	if err != nil {
		return err
	}
	byTerm := map[int64][]store.ClozeItemRef{}
	unanswered := map[int64]bool{}
	for _, ref := range refs {
		byTerm[ref.TermID] = append(byTerm[ref.TermID], ref)
		unanswered[ref.TermID] = unanswered[ref.TermID] || ref.Attempts == 0
	}
	var queries []store.ClozeQuery
	query := map[int64]int{} // слово → номер его запроса в queries
	for _, t := range targets {
		if !unanswered[t.TermID] && !t.Checked {
			query[t.TermID] = len(queries)
			queries = append(queries, store.ClozeQuery{Words: append([]string{t.Lemma}, lemma.Forms(t.Lemma)...), LessonID: t.LessonID})
		}
	}
	lessonTexts, err := a.store.ClozeLessonTexts(ctx, userID, queries)
	if err != nil {
		return err
	}
	if len(queries) > 0 && !b.wordsLoaded {
		if b.words, err = a.store.ClozeWords(ctx, userID); err != nil {
			return err
		}
		b.wordsLoaded = true
	}
	var misses []int64
	for _, t := range targets {
		if b.full() {
			break
		}
		own := byTerm[t.TermID]
		if unanswered[t.TermID] {
			if ref, ok := b.pick(own, false); ok {
				b.add(ref.ID, ref.Sentence)
			}
			continue
		}
		if !t.Checked {
			found, err := a.newClozeItem(ctx, userID, t, own, b.words, lessonTexts[query[t.TermID]], b.sentences)
			switch {
			case err != nil:
				return err
			case found.id != 0:
				b.add(found.id, found.sentence)
				continue
			case !found.busy:
				misses = append(misses, t.TermID)
			}
		}
		if ref, ok := b.pick(own, true); ok {
			b.add(ref.ID, ref.Sentence)
		}
	}
	return a.store.RecordClozeMisses(ctx, userID, misses, now)
}

// clozeNew — итог поиска нового пункта слова: id и предложение нового
// пункта (id 0 — не нашлось); busy — годное новое предложение было, но
// оно уже в выдаче: это не промах, слово получит его в другой выдаче.
type clozeNew struct {
	id       int64
	sentence string
	busy     bool
}

// newClozeItem — новый пункт слова из первого предложения его текстов
// (уроков, затем вне уроков), которое годится (cloze.Find), ещё не стало
// пунктом слова и не занято выдачей (taken). Слово убрали из словаря —
// пункта нет.
func (a *API) newClozeItem(ctx context.Context, userID int64, t store.ClozeTarget, own []store.ClozeItemRef,
	words []cloze.Word, lessonTexts []store.ClozeText, taken map[string]bool) (clozeNew, error) {
	used := make(map[string]bool, len(own))
	for _, ref := range own {
		used[string(ref.ContextHash)] = true
	}
	var res clozeNew
	gone := false
	try := func(texts []store.ClozeText) error {
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
				if taken[sentence] {
					res.busy = true
					continue
				}
				id, err := a.store.CreateClozeItem(ctx, &store.ClozeItem{
					UserID: userID, TermID: t.TermID, ClozeRef: text.ClozeRef,
					Sentence: sentence, Blank: b, ContextHash: hash, Distractors: distractors,
				})
				switch {
				case errors.Is(err, store.ErrNotFound):
					gone = true
					return nil
				case err != nil:
					return err
				}
				res.id, res.sentence = id, sentence
				return nil
			}
		}
		return nil
	}
	if err := try(lessonTexts); err != nil || res.id != 0 || gone {
		return res, err
	}
	other, err := a.store.ClozeOtherTexts(ctx, userID, t.Lemma)
	if err != nil {
		return res, err
	}
	err = try(other)
	return res, err
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
	// Attribution — подпись предложения Tatoeba со ссылками на его
	// страницу и перевод (CC BY); до ответа их нет: там предложение
	// целиком. null у остальных
	Attribution *exampleAttribution `json:"attribution"`
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
	if e := it.Example; e != nil {
		resp.Translation = &e.Translation
		resp.Attribution = &exampleAttribution{
			Author: e.Author, URL: tatoeba.SentenceURL(e.ID),
			TranslationAuthor: e.TranslationAuthor, TranslationURL: tatoeba.SentenceURL(e.TranslationID),
			License: tatoeba.License, LicenseURL: tatoeba.LicenseURL,
		}
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
	// LLMError — почему пункты без кэша получили дистракторы словаря, а не
	// LLM: rate_limited, llm_unavailable, llm_failed; null — вызов прошёл
	// или не понадобился
	LLMError *string `json:"llm_error"`
}

// handleClozeDistractors — POST /practice/cloze/distractors {item_ids}
// (S5-8, часть 2): окончательные варианты своих пунктов выдачи. Набор у
// пункта один (ADR-037): уже закреплённый отдаётся как есть, запись кэша
// закрепляется, а пункты без набора и без кэша идут в один вызов LLM.
// Варианты проверяет cloze.ParseDistractors; годные кладутся в общий кэш
// по слову и предложению (повтор провайдера не зовёт и лимит не тратит).
// Негодный ответ модели, сбой провайдера или исчерпанный лимит — у пункта
// закрепляются дистракторы словаря, ответ — 200 с llm_error. Лимит —
// суточный счётчик «объясни» (тот же вид дешёвых словесных вызовов,
// ADR-037); без ключа — 503, как у генерации и «объясни», а выдача тогда
// сразу отдаёт дистракторы словаря и сюда не ведёт.
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
	now := a.now()
	items, err := a.store.ClozeItems(ctx, u.ID, req.ItemIDs, now)
	if err != nil {
		internalError(w, "cloze items", err)
		return
	}
	if len(items) != len(req.ItemIDs) {
		writeError(w, http.StatusNotFound, "not_found", "пункт не найден")
		return
	}
	var list, fromCache []int64
	var need []int // номера пунктов без набора и без LLM-дистракторов в кэше
	var batch []cloze.LLMItem
	for i := range items {
		switch it := &items[i]; {
		case it.OptionSource != "":
		case it.LLM != nil:
			fromCache = append(fromCache, it.ID)
		default:
			need = append(need, i)
			batch = append(batch, cloze.NewLLMItem(strconv.Itoa(len(batch)+1), it.Sentence, it.Blank, it.Lemma))
		}
	}
	var llmError *string
	if len(batch) > 0 {
		got, model, code, err := a.clozeLLMDistractors(ctx, w, u.ID, batch)
		if err != nil {
			internalError(w, "cloze distractors", err)
			return
		}
		if code != "" {
			llmError = &code
		}
		for k, i := range need {
			opts, ok := got[batch[k].ID]
			if !ok {
				list = append(list, items[i].ID)
				continue
			}
			// Оплаченный ответ сохраняем, даже если клиент уже ушёл
			if err := a.store.SaveClozeDistractors(context.WithoutCancel(ctx), items[i].TermID, items[i].ContextHash,
				opts, a.llm.Name(), model); err != nil {
				internalError(w, "save cloze distractors", err)
				return
			}
			fromCache = append(fromCache, items[i].ID)
		}
	}
	if items, err = a.lockClozeOptions(context.WithoutCancel(ctx), u.ID, items, list, fromCache, now); err != nil {
		internalError(w, "cloze options", err)
		return
	}
	resp := clozeDistractorsResponse{Items: make([]clozeOptionsResponse, 0, len(items)), LLMError: llmError}
	for i := range items {
		options, source := clozeOptions(&items[i])
		resp.Items = append(resp.Items, clozeOptionsResponse{ID: items[i].ID, Options: options, DistractorSource: source})
	}
	writeJSON(w, http.StatusOK, resp)
}

// clozeLLMDistractors — LLM-дистракторы пачки одним вызовом на суточный
// счётчик «объясни»: проверенные варианты по id пункта и модель. code —
// почему вариантов LLM нет (rate_limited, llm_unavailable, llm_failed):
// тогда пункты получают дистракторы словаря. err — сбой базы.
func (a *API) clozeLLMDistractors(ctx context.Context, w http.ResponseWriter, userID int64,
	batch []cloze.LLMItem) (map[string][]string, string, string, error) {
	ok, err := a.store.ReserveLLMRequest(ctx, userID, store.LLMKindExplain, a.llmExplainDailyLimit)
	switch {
	case err != nil:
		return nil, "", "", err
	case !ok:
		return nil, "", "rate_limited", nil
	}
	// Вызов может длиться дольше WriteTimeout сервера (20 с), как
	// у «объясни»
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(explainBudget + 5*time.Second))
	lctx, cancel := context.WithTimeout(ctx, explainBudget)
	defer cancel()
	got, model, err := cloze.Generate(lctx, a.llm, batch)
	switch {
	case llm.Temporary(err):
		logf("cloze distractors user %d: %v", userID, err)
		return nil, "", "llm_unavailable", nil
	case err != nil:
		logf("cloze distractors user %d: %v", userID, err)
		return nil, "", "llm_failed", nil
	}
	if rejected := len(batch) - len(got); rejected > 0 {
		logf("cloze distractors user %d: ответ LLM не прошёл проверку у %d из %d пунктов", userID, rejected, len(batch))
	}
	return got, model, "", nil
}
