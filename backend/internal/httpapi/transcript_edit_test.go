package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/IEZhu/class/backend/internal/storage"
	"github.com/IEZhu/class/backend/internal/store"
)

// rendered — транскрипт урока прошёл всю цепочку S2-1…S2-3: реплики
// asrCompleted в базе, VTT в хранилище, очередь пуста.
func (s *asrStand) rendered(t *testing.T) {
	t.Helper()
	s.asr.respond(asrCompleted, 0)
	s.fetched(t)
	s.run(t, store.JobVTTRender)
	s.expectQueue(t)
}

func (s *asrStand) viewer(t *testing.T, cookie *http.Cookie) transcriptResponse {
	t.Helper()
	w := securityRequest(s.h, "GET", fmt.Sprintf("/lessons/%d/transcript", s.lessonID), "", cookie)
	expectStatus(t, w, http.StatusOK)
	var resp transcriptResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

// dueNow проматывает отложенный перерендер VTT (vttRerenderDelay).
func (s *asrStand) dueNow(t *testing.T) {
	t.Helper()
	if _, err := s.conn.Exec(context.Background(), `UPDATE jobs SET run_at = now()`); err != nil {
		t.Fatal(err)
	}
}

func decodeUtterance(t *testing.T, body []byte) utteranceResponse {
	t.Helper()
	var u utteranceResponse
	if err := json.Unmarshal(body, &u); err != nil {
		t.Fatal(err)
	}
	return u
}

// S2-4: teacher урока правит текст реплики — правка сохраняется с автором
// и временем, VTT перерендеривается одной джобой на пачку правок, повтор
// того же текста ничего не меняет. Остальным правка закрыта.
func TestUtteranceEdit(t *testing.T) {
	s := newASRStand(t, 0)
	ctx := context.Background()
	s.rendered(t)
	path := fmt.Sprintf("/lessons/%d/transcript/utterances/1", s.lessonID)

	// Чужие правки не проходят и ничего не ставят в очередь
	denied := []struct {
		name   string
		cookie *http.Cookie
		want   int
	}{
		{"участник", s.student, http.StatusForbidden},
		{"чужой teacher", s.otherTeacher, http.StatusForbidden},
		{"админ", s.admin, http.StatusForbidden},
		{"чужой студент", s.outsider, http.StatusForbidden},
		{"без входа", nil, http.StatusUnauthorized},
	}
	for _, c := range denied {
		if w := securityRequest(s.h, "PATCH", path, `{"text":"hijacked"}`, c.cookie); w.Code != c.want {
			t.Errorf("%s: %d, хотели %d (%s)", c.name, w.Code, c.want, w.Body.String())
		}
	}
	for _, c := range []struct {
		path, body string
		want       int
	}{
		{"/lessons/999999/transcript/utterances/1", `{"text":"x"}`, http.StatusNotFound},
		{fmt.Sprintf("/lessons/%d/transcript/utterances/99", s.lessonID), `{"text":"x"}`, http.StatusNotFound},
		{fmt.Sprintf("/lessons/%d/transcript/utterances/-1", s.lessonID), `{"text":"x"}`, http.StatusBadRequest},
		{fmt.Sprintf("/lessons/%d/transcript/utterances/one", s.lessonID), `{"text":"x"}`, http.StatusBadRequest},
		{path, `{"text":"  \n\t "}`, http.StatusBadRequest},
		{path, `{}`, http.StatusBadRequest},
		{path, `{"text":null}`, http.StatusBadRequest},
		{path, `{"text":"bad\u0000byte"}`, http.StatusBadRequest},
		{path, `{"text":"` + strings.Repeat("a", maxUtteranceRunes+1) + `"}`, http.StatusBadRequest},
		{path, `{"text":`, http.StatusBadRequest},
		{path, `{"text":"a"} {"text":"b"}`, http.StatusBadRequest},
	} {
		if w := securityRequest(s.h, "PATCH", c.path, c.body, s.teacher); w.Code != c.want {
			t.Errorf("PATCH %s %.40s: %d, хотели %d (%s)", c.path, c.body, w.Code, c.want, w.Body.String())
		}
	}
	s.expectQueue(t)
	if got := s.viewer(t, s.student).Utterances[1]; got.Text != "Morning! Is it <b>today</b> & now?" || got.EditedAt != nil {
		t.Fatalf("после отказов реплика %+v", got)
	}

	// Правка: пробелы схлопнуты, отметка и автор записаны, VTT ждёт перерендера
	w := securityRequest(s.h, "PATCH", path, `{"text":"  Morning!\nIs it   today & now?  "}`, s.teacher)
	expectStatus(t, w, http.StatusOK)
	edited := decodeUtterance(t, w.Body.Bytes())
	if edited.Idx != 1 || edited.Text != "Morning! Is it today & now?" || edited.EditedAt == nil ||
		edited.SpeakerLabel != "B" || edited.StartMS != 2300 || edited.EndMS != 3900 {
		t.Fatalf("ответ на правку %+v", edited)
	}
	var editor *int64
	if err := s.conn.QueryRow(ctx, `SELECT u.edited_by FROM utterances u JOIN transcripts t ON t.id = u.transcript_id
		WHERE t.lesson_id = $1 AND u.idx = 1`, s.lessonID).Scan(&editor); err != nil || editor == nil || *editor != s.teacherID {
		t.Fatalf("edited_by = %v, хотели %d (%v)", editor, s.teacherID, err)
	}
	s.expectQueue(t, store.JobVTTRender)

	// Вторая правка до перерендера — та же джоба, а не вторая
	other := fmt.Sprintf("/lessons/%d/transcript/utterances/0", s.lessonID)
	expectStatus(t, securityRequest(s.h, "PATCH", other, `{"text":"Good morning, everyone."}`, s.teacher), http.StatusOK)
	s.expectQueue(t, store.JobVTTRender)

	// Повтор того же текста — 200 без новой отметки и без джобы
	w = securityRequest(s.h, "PATCH", path, `{"text":"Morning! Is it today & now?"}`, s.teacher)
	expectStatus(t, w, http.StatusOK)
	if again := decodeUtterance(t, w.Body.Bytes()); again.EditedAt == nil || !again.EditedAt.Equal(*edited.EditedAt) {
		t.Fatalf("повтор сдвинул отметку правки: %v → %v", edited.EditedAt, again.EditedAt)
	}
	s.expectQueue(t, store.JobVTTRender)

	// Перерендер выкладывает VTT с правками поверх прежнего
	s.dueNow(t)
	s.run(t, store.JobVTTRender)
	s.expectQueue(t)
	obj, ok := s.files.Object(storage.TranscriptKey(s.lessonID))
	want := "WEBVTT\n" +
		"\n00:00:00.250 --> 00:00:02.100\n<v A>Good morning, everyone.\n" +
		"\n00:00:02.300 --> 00:00:03.900\n<v B>Morning! Is it today &amp; now?\n" +
		"\n00:00:04.000 --> 01:02:05.120\n<v A>Yes.\n"
	if !ok || string(obj.Body) != want {
		t.Fatalf("VTT после правки (%v): %q", ok, obj.Body)
	}
	// Правку видят все, кому открыт урок
	resp := s.viewer(t, s.student)
	if got := resp.Utterances[1]; got.Text != "Morning! Is it today & now?" || got.EditedAt == nil {
		t.Errorf("участник видит реплику %+v", got)
	}
	if got := resp.Utterances[2]; got.EditedAt != nil {
		t.Errorf("неправленая реплика с отметкой: %+v", got)
	}
}

// S2-5: teacher урока сопоставляет метку спикера с собой или участником
// урока — все реплики метки получают user_id, вьюер показывает имя.
// Посторонний человек — 400, неизвестная метка — 404, повтор — 200.
func TestSpeakerMapping(t *testing.T) {
	s := newASRStand(t, 0)
	s.rendered(t)
	speaker := func(label string) string {
		return fmt.Sprintf("/lessons/%d/transcript/speakers/%s", s.lessonID, label)
	}
	body := func(id int64) string { return fmt.Sprintf(`{"user_id":%d}`, id) }

	for _, c := range []struct {
		name   string
		cookie *http.Cookie
		want   int
	}{
		{"участник", s.student, http.StatusForbidden},
		{"чужой teacher", s.otherTeacher, http.StatusForbidden},
		{"админ", s.admin, http.StatusForbidden},
		{"чужой студент", s.outsider, http.StatusForbidden},
		{"без входа", nil, http.StatusUnauthorized},
	} {
		if w := securityRequest(s.h, "PUT", speaker("A"), body(s.studentID), c.cookie); w.Code != c.want {
			t.Errorf("%s: %d, хотели %d (%s)", c.name, w.Code, c.want, w.Body.String())
		}
	}
	for _, c := range []struct {
		name, path, body string
		want             int
	}{
		{"чужой студент", speaker("A"), body(s.outsiderID), http.StatusBadRequest},
		{"чужой teacher", speaker("A"), body(s.otherTeacherID), http.StatusBadRequest},
		{"нет такого человека", speaker("A"), body(999999), http.StatusBadRequest},
		{"нулевой id", speaker("A"), `{"user_id":0}`, http.StatusBadRequest},
		{"строка", speaker("A"), `{"user_id":"1"}`, http.StatusBadRequest},
		{"без user_id", speaker("A"), `{}`, http.StatusBadRequest},
		{"невалидный JSON", speaker("A"), `{"user_id":`, http.StatusBadRequest},
		{"нет такой метки", speaker("Z"), body(s.studentID), http.StatusNotFound},
		{"нет урока", "/lessons/999999/transcript/speakers/A", body(s.studentID), http.StatusNotFound},
	} {
		if w := securityRequest(s.h, "PUT", c.path, c.body, s.teacher); w.Code != c.want {
			t.Errorf("%s: %d, хотели %d (%s)", c.name, w.Code, c.want, w.Body.String())
		}
	}
	for _, u := range s.viewer(t, s.teacher).Utterances {
		if u.UserID != nil || u.SpeakerName != nil {
			t.Fatalf("после отказов реплика сопоставлена: %+v", u)
		}
	}

	// A — teacher урока, B — участник; повтор того же маппинга — 200
	for _, m := range []struct {
		label string
		id    int64
		name  string
	}{{"A", s.teacherID, "asr-teacher"}, {"B", s.studentID, "asr-student"}, {"B", s.studentID, "asr-student"}} {
		w := securityRequest(s.h, "PUT", speaker(m.label), body(m.id), s.teacher)
		expectStatus(t, w, http.StatusOK)
		if got, want := strings.TrimSpace(w.Body.String()),
			fmt.Sprintf(`{"speaker_label":%q,"user_id":%d,"speaker_name":%q}`, m.label, m.id, m.name); got != want {
			t.Errorf("ответ %s, хотели %s", got, want)
		}
	}
	// Имена видит и участник: доступ к транскрипту — как к уроку
	names := []string{}
	for _, u := range s.viewer(t, s.student).Utterances {
		if u.SpeakerName == nil || u.UserID == nil {
			t.Fatalf("реплика без имени после маппинга: %+v", u)
		}
		names = append(names, u.SpeakerLabel+"="+*u.SpeakerName)
	}
	if got := strings.Join(names, ","); got != "A=asr-teacher,B=asr-student,A=asr-teacher" {
		t.Errorf("спикеры во вьюере: %s", got)
	}
	for _, c := range []*http.Cookie{s.outsider, s.otherTeacher, s.admin} {
		expectStatus(t, securityRequest(s.h, "GET", fmt.Sprintf("/lessons/%d/transcript", s.lessonID), "", c), http.StatusForbidden)
	}

	// null снимает маппинг; VTT несёт метки, поэтому перерендера нет
	w := securityRequest(s.h, "PUT", speaker("B"), `{"user_id":null}`, s.teacher)
	expectStatus(t, w, http.StatusOK)
	if got := strings.TrimSpace(w.Body.String()); got != `{"speaker_label":"B","user_id":null,"speaker_name":null}` {
		t.Errorf("снятие маппинга: %s", got)
	}
	if u := s.viewer(t, s.teacher).Utterances[1]; u.UserID != nil || u.SpeakerName != nil {
		t.Errorf("маппинг B не снят: %+v", u)
	}
	expectStatus(t, securityRequest(s.h, "PUT", speaker("B"), `{"user_id":null}`, s.teacher), http.StatusOK)
	s.expectQueue(t)
}

// Правка реплики и маппинг спикера пишут только в транскрипт урока из
// пути: у второго урока (другой teacher) те же idx и метки, и его
// реплики не меняются — ни текст, ни отметка правки, ни user_id, — а VTT
// его транскрипта не перерендеривается. idx и метка, которые есть только
// у второго урока, — 404.
func TestTranscriptWritesStayInLesson(t *testing.T) {
	s := newASRStand(t, 0)
	ctx := context.Background()
	s.rendered(t)
	var otherLesson, otherTranscript int64
	if err := s.conn.QueryRow(ctx, `INSERT INTO lessons (group_id, title, teacher_id, starts_at, ends_at)
		VALUES (NULL, 'Другой урок', $1, now(), now() + interval '1 hour') RETURNING id`, s.otherTeacherID).Scan(&otherLesson); err != nil {
		t.Fatal(err)
	}
	if err := s.conn.QueryRow(ctx, `INSERT INTO transcripts (lesson_id, provider, status, external_id)
		VALUES ($1, 'assemblyai', 'completed', 'tr_other') RETURNING id`, otherLesson).Scan(&otherTranscript); err != nil {
		t.Fatal(err)
	}
	if _, err := s.conn.Exec(ctx, `INSERT INTO utterances (transcript_id, idx, speaker_label, start_ms, end_ms, text)
		VALUES ($1, 0, 'A', 0, 10, 'other zero'), ($1, 1, 'A', 10, 20, 'other one'), ($1, 5, 'C', 20, 30, 'other five')`,
		otherTranscript); err != nil {
		t.Fatal(err)
	}
	other := func() string {
		t.Helper()
		var state string
		if err := s.conn.QueryRow(ctx, `SELECT string_agg(format('%s:%s:%s:%s', idx, text, user_id, edited_at), '|' ORDER BY idx)
			FROM utterances WHERE transcript_id = $1`, otherTranscript).Scan(&state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	before := other()

	for _, c := range []struct {
		name, method, path, body string
		want                     int
	}{
		{"правка idx, который есть у обоих", "PATCH", "/transcript/utterances/1", `{"text":"Edited here only"}`, http.StatusOK},
		{"правка idx только второго урока", "PATCH", "/transcript/utterances/5", `{"text":"Not mine"}`, http.StatusNotFound},
		{"маппинг метки, которая есть у обоих", "PUT", "/transcript/speakers/A", fmt.Sprintf(`{"user_id":%d}`, s.teacherID), http.StatusOK},
		{"маппинг метки только второго урока", "PUT", "/transcript/speakers/C", fmt.Sprintf(`{"user_id":%d}`, s.teacherID), http.StatusNotFound},
	} {
		w := securityRequest(s.h, c.method, fmt.Sprintf("/lessons/%d%s", s.lessonID, c.path), c.body, s.teacher)
		if w.Code != c.want {
			t.Errorf("%s: %d, хотели %d (%s)", c.name, w.Code, c.want, w.Body.String())
		}
		if got := other(); got != before {
			t.Fatalf("%s: второй урок изменился: %s, было %s", c.name, got, before)
		}
	}
	if u := s.viewer(t, s.teacher).Utterances[1]; u.Text != "Edited here only" {
		t.Errorf("своя правка не сохранилась: %+v", u)
	}
	var otherRenders int
	if err := s.conn.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = $1 AND payload = $2`,
		store.JobVTTRender, store.TranscriptJob{TranscriptID: otherTranscript}).Scan(&otherRenders); err != nil || otherRenders != 0 {
		t.Fatalf("перерендеров VTT второго урока: %d (%v)", otherRenders, err)
	}
}

// 0014 добавляет к utterances отметки правки; откат их убирает, текст
// правки остаётся, повторный накат возвращает колонки. 0022 (S4-4)
// надстраивает utterances хранимыми лексемами — её снимаем первой и
// накатываем последней, как golang-migrate.
func TestUtteranceEditsMigrationReverts(t *testing.T) {
	s := newASRStand(t, 0)
	ctx := context.Background()
	s.rendered(t)
	expectStatus(t, securityRequest(s.h, "PATCH", fmt.Sprintf("/lessons/%d/transcript/utterances/2", s.lessonID),
		`{"text":"Yes, exactly."}`, s.teacher), http.StatusOK)
	columns := func() string {
		rows, err := s.conn.Query(ctx, `SELECT column_name FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = 'utterances' ORDER BY ordinal_position`)
		if err != nil {
			t.Fatal(err)
		}
		names, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(names, ",")
	}
	apply := func(file string) {
		t.Helper()
		sql, err := os.ReadFile("../../migrations/" + file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.conn.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
	}
	const base = "transcript_id,idx,speaker_label,user_id,start_ms,end_ms,text"
	if got := columns(); got != base+",edited_at,edited_by,text_tsv" {
		t.Fatalf("utterances: %s", got)
	}
	apply("0022_cloze.down.sql")
	if got := columns(); got != base+",edited_at,edited_by" {
		t.Fatalf("после отката 0022: %s", got)
	}
	apply("0014_utterance_edits.down.sql")
	if got := columns(); got != base {
		t.Fatalf("после отката: %s", got)
	}
	var text string
	if err := s.conn.QueryRow(ctx, `SELECT text FROM utterances WHERE idx = 2`).Scan(&text); err != nil || text != "Yes, exactly." {
		t.Fatalf("после отката текст %q (%v)", text, err)
	}
	apply("0014_utterance_edits.up.sql")
	if got := columns(); got != base+",edited_at,edited_by" {
		t.Fatalf("после повторного наката: %s", got)
	}
	_, err := s.conn.Exec(ctx, `UPDATE utterances SET edited_by = $1 WHERE idx = 0`, s.teacherID)
	if store.PgErrorCode(err) != pgCheckViolation {
		t.Fatalf("автор правки без времени: %v, хотели check violation", err)
	}
	apply("0022_cloze.up.sql")
	if got := columns(); got != base+",edited_at,edited_by,text_tsv" {
		t.Fatalf("после повторного наката 0022: %s", got)
	}
}
