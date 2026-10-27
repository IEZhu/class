package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/IEZhu/class/backend/internal/lemma"
	"github.com/IEZhu/class/backend/internal/store"
)

const (
	// maxVideoWords — целей поиска за запрос: каждая — проход по GIN-индексу
	// субтитров (замер — ADR-038)
	maxVideoWords = 50
	// Страница GET /practice/videos: по умолчанию и потолок
	defaultVideosLimit = 10
	maxVideosLimit     = 50
	// maxVideosOffset — дальше подбор не листается: столько видео под
	// один список слов не посмотреть
	maxVideosOffset = 1000
	// videoBestSegments — лучших мест на видео в выдаче
	videoBestSegments = 5
)

// weightsWords — цели из ?words= запроса, у каждой вес 1.
const weightsWords = "words"

// learningWordSource — невыученные слова вызывающего и их веса для
// видео-практики (ADR-038). Реализация — store.LearningWords: по
// карточкам FSRS, когда S4-1 завела srs_cards, иначе по статусу.
// Ранжирование не знает, откуда веса: источник подменяется целиком.
type learningWordSource interface {
	LearningWords(ctx context.Context, userID int64, lang string, limit int) ([]store.LearningWord, string, error)
}

type videoWordResponse struct {
	Lemma  string  `json:"lemma"`
	Weight float64 `json:"weight"`
}

type videoSegmentResponse struct {
	Idx     int    `json:"idx"`
	StartMS int    `json:"start_ms"`
	EndMS   int    `json:"end_ms"`
	Text    string `json:"text"`
	// Words — леммы целей, найденных в сегменте
	Words []string `json:"words"`
}

type practiceVideoResponse struct {
	ID int64 `json:"id"`
	// YouTubeID — id ролика для встраивания; URL — страница ролика на
	// YouTube (подпись источника под плеером, ADR-038)
	YouTubeID   string                 `json:"youtube_id"`
	URL         string                 `json:"url"`
	Title       string                 `json:"title"`
	Channel     *string                `json:"channel"`
	DurationMS  int                    `json:"duration_ms"`
	Subtitles   string                 `json:"subtitles"`
	Coverage    float64                `json:"coverage"`
	Occurrences int                    `json:"occurrences"`
	PerMinute   float64                `json:"per_minute"`
	Words       []string               `json:"matched_words"`
	Segments    []videoSegmentResponse `json:"segments"`
}

// videoTargetsResponse — цели запроса: откуда веса (words | srs | status)
// и сами слова в порядке приоритета.
type videoTargetsResponse struct {
	Weighting string              `json:"weighting"`
	Words     []videoWordResponse `json:"words"`
}

type practiceVideosPage struct {
	videoTargetsResponse
	Items []practiceVideoResponse `json:"items"`
	// NextCursor — для ?cursor= следующей страницы; null — страница последняя
	NextCursor *string `json:"next_cursor"`
}

type practiceVideoDetail struct {
	videoTargetsResponse
	Video practiceVideoResponse `json:"video"`
}

func lemmasOf(targets []store.VideoTarget, idx []int) []string {
	out := make([]string, 0, len(idx))
	for _, i := range idx {
		out = append(out, targets[i].Lemma)
	}
	return out
}

func toPracticeVideoResponse(v *store.PracticeVideo, targets []store.VideoTarget) practiceVideoResponse {
	resp := practiceVideoResponse{
		ID: v.ID, YouTubeID: v.ExternalID, URL: "https://www.youtube.com/watch?v=" + url.QueryEscape(v.ExternalID),
		Title: v.Title, Channel: v.Channel, DurationMS: v.DurationMS, Subtitles: v.Subtitles,
		Coverage: v.Coverage, Occurrences: v.Occurrences, PerMinute: v.PerMinute,
		Words: lemmasOf(targets, v.Words), Segments: make([]videoSegmentResponse, 0, len(v.Segments)),
	}
	for _, s := range v.Segments {
		resp.Segments = append(resp.Segments, videoSegmentResponse{
			Idx: s.Idx, StartMS: s.StartMS, EndMS: s.EndMS, Text: s.Text, Words: lemmasOf(targets, s.Words),
		})
	}
	return resp
}

// videoTargets — цели подбора: слова из ?words= (через запятую или
// повтором параметра, вес 1) или, без них, невыученные слова словаря
// вызывающего с весами. Цель — лемма и её неправильные формы
// (lemma.Forms): «went» в субтитрах находится по слову go. Ошибка
// запроса — ответ 400 уже записан, ok = false.
func (a *API) videoTargets(w http.ResponseWriter, r *http.Request) (_ []store.VideoTarget, _ videoTargetsResponse, ok bool) {
	var words []store.LearningWord
	weighting := weightsWords
	seen := map[string]bool{}
	for _, v := range r.URL.Query()["words"] {
		for part := range strings.SplitSeq(v, ",") {
			word := strings.TrimSpace(part)
			if word == "" {
				continue
			}
			if !lemma.IsWord(word) {
				writeError(w, http.StatusBadRequest, "bad_request", "words — английские слова через запятую")
				return nil, videoTargetsResponse{}, false
			}
			if l := lemma.Lemma(word); !seen[l] {
				seen[l] = true
				words = append(words, store.LearningWord{Lemma: l, Weight: 1})
			}
		}
	}
	if len(words) > maxVideoWords {
		writeError(w, http.StatusBadRequest, "bad_request", "words — не больше 50 слов")
		return nil, videoTargetsResponse{}, false
	}
	if len(words) == 0 {
		var err error
		words, weighting, err = a.learningWords.LearningWords(r.Context(), userFrom(r.Context()).ID, termLang, maxVideoWords)
		if err != nil {
			internalError(w, "learning words", err)
			return nil, videoTargetsResponse{}, false
		}
	}
	targets := make([]store.VideoTarget, 0, len(words))
	resp := videoTargetsResponse{Weighting: weighting, Words: make([]videoWordResponse, 0, len(words))}
	for _, word := range words {
		// Лемма словаря, которую клик не дал бы (мусор старых строк), и
		// неположительный вес в поиск не идут
		if !lemma.IsWord(word.Lemma) || word.Weight <= 0 {
			continue
		}
		targets = append(targets, store.VideoTarget{
			Lemma: word.Lemma, Forms: append([]string{word.Lemma}, lemma.Forms(word.Lemma)...), Weight: word.Weight,
		})
		resp.Words = append(resp.Words, videoWordResponse{Lemma: word.Lemma, Weight: word.Weight})
	}
	return targets, resp, true
}

// handlePracticeVideos — GET /practice/videos?words=&limit=&cursor=
// (S5-5, ADR-038): видео корпуса субтитров, ранжированные по доле веса
// найденных целевых слов, затем по их плотности на минуту, у каждого —
// лучшие места с таймкодами. Без ?words= цели — из словаря вызывающего;
// пустой словарь — пустой список.
func (a *API) handlePracticeVideos(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := defaultVideosLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxVideosLimit {
			writeError(w, http.StatusBadRequest, "bad_request", "limit — от 1 до 50")
			return
		}
		limit = n
	}
	// Курсор — позиция в выдаче: ранжирование считается на каждый запрос
	// целиком, сдвиг по нему ничего не стоит (ADR-038)
	offset := 0
	if v := q.Get("cursor"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > maxVideosOffset {
			writeError(w, http.StatusBadRequest, "bad_request", "некорректный cursor")
			return
		}
		offset = n
	}
	targets, words, ok := a.videoTargets(w, r)
	if !ok {
		return
	}
	// Строка сверх страницы — признак, что следующая страница есть
	videos, err := a.store.PracticeVideos(r.Context(), targets, limit+1, offset, videoBestSegments)
	if err != nil {
		internalError(w, "practice videos", err)
		return
	}
	page := practiceVideosPage{videoTargetsResponse: words, Items: make([]practiceVideoResponse, 0, min(len(videos), limit))}
	if len(videos) > limit {
		videos = videos[:limit]
		if next := offset + limit; next <= maxVideosOffset {
			cursor := strconv.Itoa(next)
			page.NextCursor = &cursor
		}
	}
	for i := range videos {
		page.Items = append(page.Items, toPracticeVideoResponse(&videos[i], targets))
	}
	writeJSON(w, http.StatusOK, page)
}

// handlePracticeVideo — GET /practice/videos/{id}?words=: видео для
// плеера практики — все сегменты субтитров по порядку с найденными
// целями (цели — как у подбора). 404 — нет видео, оно не с YouTube или
// без субтитров.
func (a *API) handlePracticeVideo(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	targets, words, ok := a.videoTargets(w, r)
	if !ok {
		return
	}
	v, err := a.store.PracticeVideoByID(r.Context(), id, targets)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "видео не найдено или без субтитров")
		return
	case err != nil:
		internalError(w, "practice video", err)
		return
	}
	writeJSON(w, http.StatusOK, practiceVideoDetail{videoTargetsResponse: words, Video: toPracticeVideoResponse(v, targets)})
}
