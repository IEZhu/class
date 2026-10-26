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

// 0014 добавляет к utterances отметки правки; откат их убирает, текст
// правки остаётся, повторный накат возвращает колонки.
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
	if got := columns(); got != base+",edited_at,edited_by" {
		t.Fatalf("utterances: %s", got)
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
}
