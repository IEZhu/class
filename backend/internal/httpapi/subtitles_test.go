package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/IEZhu/class/backend/internal/harvester"
	"github.com/IEZhu/class/backend/internal/jobs"
	"github.com/IEZhu/class/backend/internal/store"
	"github.com/IEZhu/class/backend/internal/worker"
)

// S5-1/S5-2 на общей тестовой базе: миграция 0011, полнотекстовый поиск
// и обе джобы сборщика через очередь, как их выполняет worker с ролью
// harvester. В YouTube тесты не ходят: фиды отдаёт httptest, yt-dlp —
// подделка из internal/harvester/testdata.

const (
	tedChannelID = "UCAuUUnT6oDeKwE6v1NGQxug"
	bbcChannelID = "UCHaHD477h-FeBbVh9Sh7syA"
	voaChannelID = "UCKyTokYo0nK2OA-az-sDijA"
)

func subtitleSourceID(t *testing.T, conn *pgx.Conn, externalID string) int64 {
	t.Helper()
	var id int64
	if err := conn.QueryRow(context.Background(),
		`SELECT id FROM subtitle_sources WHERE external_id = $1`, externalID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func applySubtitlesMigration(t *testing.T, conn *pgx.Conn, name string) {
	t.Helper()
	sql, err := os.ReadFile("../../migrations/" + name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(context.Background(), string(sql)); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

// runHarvesterJob ставит джобу сборщика и выполняет её свежим процессом
// сборщика — как worker с WORKER_ROLE=harvester.
func runHarvesterJob(t *testing.T, a *API, kind string, h *harvester.Harvester) {
	t.Helper()
	ctx := context.Background()
	runner := jobs.NewRunner(a.store.Pool(), jobs.Config{WorkerID: "harvester-test", HandlerTimeout: harvester.JobTimeout})
	worker.RegisterHarvester(runner, jobs.NewScheduler(a.store.Pool()), h)
	if _, err := jobs.Enqueue(ctx, a.store.Pool(), kind, struct{}{}); err != nil {
		t.Fatal(err)
	}
	if worked, err := runner.RunOnce(ctx); err != nil || !worked {
		t.Fatalf("RunOnce = %v, %v", worked, err)
	}
	var left int
	if err := a.store.Pool().QueryRow(ctx, `SELECT count(*) FROM jobs`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("job %s not done: %d left, %v", kind, left, err)
	}
}

func TestSubtitlesMigrationIndexesAndReverts(t *testing.T) {
	_, conn := securityDB(t)
	ctx := context.Background()
	var generated, expr string
	if err := conn.QueryRow(ctx,
		`SELECT a.attgenerated::text, pg_get_expr(d.adbin, d.adrelid)
		 FROM pg_attribute a JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
		 WHERE a.attrelid = 'subtitle_segments'::regclass AND a.attname = 'tsv'`).Scan(&generated, &expr); err != nil {
		t.Fatal(err)
	}
	if generated != "s" || !strings.Contains(expr, "to_tsvector('english'::regconfig, text)") {
		t.Fatalf("tsv is %q %q, want a stored to_tsvector('english', text)", generated, expr)
	}
	var index string
	if err := conn.QueryRow(ctx,
		`SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema()
		 AND indexname = 'subtitle_segments_tsv_idx'`).Scan(&index); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(index, "USING gin (tsv)") {
		t.Fatalf("index %q", index)
	}

	seed := func() string {
		t.Helper()
		rows, err := conn.Query(ctx, `SELECT kind || ':' || external_id || ':' || title
			FROM subtitle_sources WHERE enabled ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		names, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(names, ",")
	}
	want := "channel:" + tedChannelID + ":TED,channel:UCsooa4yRKGN_zEE8iknghZA:TED-Ed," +
		"channel:" + bbcChannelID + ":BBC Learning English,channel:" + voaChannelID + ":VOA Learning English"
	if got := seed(); got != want {
		t.Fatalf("seed %s, want %s", got, want)
	}

	tables := func() string {
		t.Helper()
		var present string
		if err := conn.QueryRow(ctx, `SELECT concat_ws(',', to_regclass('job_schedules'),
			to_regclass('subtitle_sources'), to_regclass('videos'), to_regclass('subtitle_segments'))`).Scan(&present); err != nil {
			t.Fatal(err)
		}
		return present
	}
	applySubtitlesMigration(t, conn, "0011_subtitles.down.sql")
	if got := tables(); got != "" {
		t.Fatalf("down left %s", got)
	}
	applySubtitlesMigration(t, conn, "0011_subtitles.up.sql")
	if got := tables(); got != "job_schedules,subtitle_sources,videos,subtitle_segments" {
		t.Fatalf("re-up created %q", got)
	}
	if got := seed(); got != want {
		t.Fatalf("seed after re-up %s", got)
	}
}

// Поиск для видео-практики: слова приводятся тем же словарём english,
// что и tsv, поэтому «running» находит «runs», а стоп-слова не ищутся.
func TestSearchSubtitlesFindsStemmedWords(t *testing.T) {
	a, conn := securityDB(t)
	ctx := context.Background()
	st := a.store
	if _, err := st.AddFeedVideos(ctx, subtitleSourceID(t, conn, tedChannelID), []store.FeedVideo{
		{ExternalID: "talkVideo01", Title: "A talk"}, {ExternalID: "talkVideo02", Title: "Another talk"},
	}); err != nil {
		t.Fatal(err)
	}
	videoIDs := map[string]int64{}
	rows, err := conn.Query(ctx, `SELECT external_id, id FROM videos`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var ext string
		var id int64
		if err := rows.Scan(&ext, &id); err != nil {
			t.Fatal(err)
		}
		videoIDs[ext] = id
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	first := []store.SubtitleSegment{
		{StartMS: 0, EndMS: 2000, Text: "The cats are running home."},
		{StartMS: 2000, EndMS: 4000, Text: "Nothing to see here."},
		{StartMS: 4000, EndMS: 6000, Text: "She runs every morning, doesn't she?"},
	}
	// Повтор сохранения (at-least-once) заменяет сегменты, а не дублирует
	for range 2 {
		if err := st.SaveSubtitles(ctx, videoIDs["talkVideo01"], store.SubtitlesManual, 125, first); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SaveSubtitles(ctx, videoIDs["talkVideo02"], store.SubtitlesAuto, 0,
		[]store.SubtitleSegment{{StartMS: 500, EndMS: 900, Text: "a cat sat on the mat"}}); err != nil {
		t.Fatal(err)
	}

	search := func(words ...string) string {
		t.Helper()
		matches, err := st.SearchSubtitles(ctx, words, 10)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, m := range matches {
			got = append(got, fmt.Sprintf("%s#%d@%d", m.ExternalID, m.Idx, m.StartMS))
		}
		return strings.Join(got, " ")
	}
	for words, want := range map[string]string{
		"running":         "talkVideo01#0@0 talkVideo01#2@4000",
		"Cats":            "talkVideo01#0@0 talkVideo02#0@500",
		"run mat":         "talkVideo01#0@0 talkVideo01#2@4000 talkVideo02#0@500",
		"ice cream":       "",
		"the":             "",
		"o'clock morning": "talkVideo01#2@4000",
	} {
		if got := search(strings.Fields(words)...); got != want {
			t.Errorf("search %q = %q, want %q", words, got, want)
		}
	}
	if got := search(); got != "" {
		t.Errorf("empty word list found %q", got)
	}

	var count int
	var duration *int
	if err := conn.QueryRow(ctx, `SELECT count(*), max(v.duration_s) FROM subtitle_segments s
		JOIN videos v ON v.id = s.video_id WHERE v.external_id = 'talkVideo01'`).Scan(&count, &duration); err != nil {
		t.Fatal(err)
	}
	if count != 3 || duration == nil || *duration != 125 {
		t.Fatalf("talkVideo01: %d segments, duration %v", count, duration)
	}

	// Поиск по tsv идёт через GIN-индекс
	if _, err := conn.Exec(ctx, `SET enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	var plan strings.Builder
	planRows, err := conn.Query(ctx,
		`EXPLAIN SELECT idx FROM subtitle_segments WHERE tsv @@ plainto_tsquery('english', 'running')`)
	if err != nil {
		t.Fatal(err)
	}
	for planRows.Next() {
		var line string
		if err := planRows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(line + "\n")
	}
	if !strings.Contains(plan.String(), "subtitle_segments_tsv_idx") {
		t.Fatalf("plan does not use the GIN index:\n%s", plan.String())
	}
}

// rss_poll: новые видео из фидов включённых источников, повтор ничего не
// дублирует; сбой одного фида записан у источника и джобу не валит.
func TestRSSPollAddsVideosIdempotently(t *testing.T) {
	a, conn := securityDB(t)
	ctx := context.Background()
	feed, err := os.ReadFile("../harvester/testdata/feed.xml")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("channel_id") {
		case bbcChannelID:
			_, _ = w.Write(feed)
		case voaChannelID:
			http.Error(w, "upstream", http.StatusInternalServerError)
		default:
			_, _ = w.Write([]byte(`<feed xmlns="http://www.w3.org/2005/Atom"><title>empty</title></feed>`))
		}
	}))
	defer srv.Close()
	h := harvester.New(a.store, harvester.Config{FeedURL: srv.URL, HTTPClient: srv.Client(),
		Fetcher: harvester.YTDLP{Path: "/nonexistent/yt-dlp"}})

	videos := func() string {
		t.Helper()
		rows, err := conn.Query(ctx, `SELECT v.external_id || '|' || v.channel || '|' || s.external_id
			|| '|' || (v.published_at IS NOT NULL) || '|' || coalesce(v.subtitles, 'pending')
			FROM videos v JOIN subtitle_sources s ON s.id = v.source_id ORDER BY v.external_id`)
		if err != nil {
			t.Fatal(err)
		}
		got, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(got, "\n")
	}
	want := strings.Join([]string{
		"VFA53-P4yPo|BBC Learning English|" + bbcChannelID + "|true|pending",
		"WXKilGiOQy8|BBC Learning English|" + bbcChannelID + "|true|pending",
		"pjFlVc-EDzc|BBC Learning English|" + bbcChannelID + "|true|pending",
	}, "\n")
	for round := range 2 {
		runHarvesterJob(t, a, harvester.JobRSSPoll, h)
		if got := videos(); got != want {
			t.Fatalf("round %d videos:\n%s\nwant:\n%s", round+1, got, want)
		}
	}

	var polled int
	var voaError string
	if err := conn.QueryRow(ctx, `SELECT count(*) FILTER (WHERE polled_at IS NOT NULL),
		max(last_error) FILTER (WHERE external_id = $1) FROM subtitle_sources`, voaChannelID).Scan(&polled, &voaError); err != nil {
		t.Fatal(err)
	}
	if polled != 4 || voaError != "фид "+voaChannelID+": HTTP 500" {
		t.Fatalf("polled %d sources, VOA error %q", polled, voaError)
	}
}

// subtitles_fetch: настоящий запуск yt-dlp (подделка) → сегменты в базе.
// Ручные приоритетнее auto, автосубтитры без дублей катящихся строк, видео
// без английских субтитров не повторяется, недоступное откладывается,
// блокировка видео не штрафует.
func TestSubtitlesFetchFillsCorpus(t *testing.T) {
	a, conn := securityDB(t)
	ctx := context.Background()
	script, err := filepath.Abs("../harvester/testdata/fake-yt-dlp")
	if err != nil {
		t.Fatal(err)
	}
	// Подделка пишет аргументы вызовов в $HOME/yt-dlp.args
	home := t.TempDir()
	t.Setenv("HOME", home)
	argsFile := filepath.Join(home, "yt-dlp.args")
	ted := subtitleSourceID(t, conn, tedChannelID)

	// Каждое видео — свежий процесс сборщика: темп внутри пакета (15–30 с
	// между видео) проверяют тесты internal/harvester без настоящих пауз
	for _, id := range []string{"manualSubs1", "autoSubs001", "noSubtitle1", "private0001", "tooMany4291"} {
		if _, err := a.store.AddFeedVideos(ctx, ted, []store.FeedVideo{{ExternalID: id, Title: id}}); err != nil {
			t.Fatal(err)
		}
		runHarvesterJob(t, a, harvester.JobSubtitlesFetch, harvester.New(a.store,
			harvester.Config{Fetcher: harvester.YTDLP{Path: script}}))
	}

	type state struct {
		subtitles, harvestError string
		duration, attempts      int
		segments                int
	}
	got := map[string]state{}
	rows, err := conn.Query(ctx, `SELECT v.external_id, coalesce(v.subtitles, ''), coalesce(v.harvest_error, ''),
		coalesce(v.duration_s, 0), v.harvest_attempts, count(s.idx)
		FROM videos v LEFT JOIN subtitle_segments s ON s.video_id = v.id GROUP BY v.id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		var s state
		if err := rows.Scan(&id, &s.subtitles, &s.harvestError, &s.duration, &s.attempts, &s.segments); err != nil {
			t.Fatal(err)
		}
		got[id] = s
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]state{
		"manualSubs1": {subtitles: store.SubtitlesManual, duration: 754, segments: 4},
		"autoSubs001": {subtitles: store.SubtitlesAuto, duration: 61, segments: 3},
		"noSubtitle1": {subtitles: store.SubtitlesNone, duration: 30},
		"tooMany4291": {},
	} {
		if got[id] != want {
			t.Errorf("%s: %+v, want %+v", id, got[id], want)
		}
	}
	if p := got["private0001"]; p.subtitles != "" || p.attempts != 1 || !strings.Contains(p.harvestError, "Private video") {
		t.Errorf("private0001: %+v", p)
	}

	matches, err := a.store.SearchSubtitles(ctx, []string{"run"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, m := range matches {
		found = append(found, fmt.Sprintf("%s#%d %s", m.ExternalID, m.Idx, m.Text))
	}
	if want := "manualSubs1#2 that you stop running for two minutes every day.|autoSubs001#1 to the cats running"; strings.Join(found, "|") != want {
		t.Fatalf("search run: %q", found)
	}

	// Следующий процесс сборщика (пауза — в памяти прошлого) снова пробует
	// только заблокированное видео: собранное и отложенное не трогает
	if err := os.Remove(argsFile); err != nil {
		t.Fatal(err)
	}
	runHarvesterJob(t, a, harvester.JobSubtitlesFetch, harvester.New(a.store,
		harvester.Config{Fetcher: harvester.YTDLP{Path: script}}))
	args, err := os.ReadFile(argsFile)
	if err != nil || !strings.HasSuffix(strings.TrimSpace(string(args)), "watch?v=tooMany4291") {
		t.Fatalf("retry called yt-dlp with %q, %v", args, err)
	}
	if got := strings.Count(string(args), "watch?v="); got != 1 {
		t.Fatalf("retry fetched %d videos, want only the blocked one", got)
	}
}
