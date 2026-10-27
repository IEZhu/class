package store

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/IEZhu/class/backend/internal/testdb"
)

// clozeHistorySQL — студент с историей размера замера ревью: 100 уроков,
// в каждом транскрипт из 500 реплик и 20 материалов. Словарь реплик —
// как у живого текста, с длинным хвостом: частые we и go в каждой
// реплике и 5000 редких слов; river — в репликах трёх уроков и в одном
// материале, ещё один материал с river ждёт загрузки файла. Чужой урок
// с river — у другого студента.
const clozeHistorySQL = `
	INSERT INTO users (email, role, name) VALUES ('t@x', 'teacher', 'T'), ('s@x', 'student', 'S'), ('o@x', 'student', 'O');
	INSERT INTO groups (name, level) VALUES ('g', 'B1');
	INSERT INTO lessons (group_id, teacher_id, starts_at, ends_at, status)
	SELECT 1, (SELECT id FROM users WHERE email = 't@x'), now() - g * interval '1 day',
	       now() - g * interval '1 day' + interval '1 hour', 'done'
	FROM generate_series(1, 101) g;
	INSERT INTO lesson_participants (lesson_id, user_id)
	SELECT l.id, u.id FROM lessons l, users u
	WHERE u.email = CASE WHEN l.id = (SELECT max(id) FROM lessons) THEN 'o@x' ELSE 's@x' END;
	INSERT INTO transcripts (lesson_id, provider, status, external_id)
	SELECT id, 'assemblyai', 'completed', 'x' || id FROM lessons;
	INSERT INTO utterances (transcript_id, idx, speaker_label, start_ms, end_ms, text)
	SELECT t.id, i, 'A', i * 1000, i * 1000 + 900,
	       'We go ' || (SELECT string_agg('w' || (i * 7919 + k * 104729 + t.id * 31) % 5000, ' ')
	                    FROM generate_series(1, 6 + i % 10) k) || '.'
	FROM transcripts t, generate_series(0, 499) i;
	UPDATE utterances u SET text = 'We walked along the river in lesson ' || tr.lesson_id || '.'
	FROM transcripts tr WHERE tr.id = u.transcript_id AND u.idx = 5 AND tr.lesson_id IN (3, 7, 101);
	INSERT INTO materials (lesson_id, kind, title, body_md)
	SELECT l.id, 'material', 'm' || k, 'We go over words of lesson ' || l.id || ': w' || (l.id * 37 + k * 11) % 5000 || '.'
	FROM lessons l, generate_series(1, 20) k;
	INSERT INTO materials (lesson_id, kind, title, body_md) VALUES (5, 'material', 'r', 'The river is wide here.');
	INSERT INTO materials (lesson_id, kind, title, body_md, s3_key, file_name, file_content_type, file_size)
	VALUES (2, 'homework', 'p', 'The river is pending here.', 'materials/2/p.pdf', 'p.pdf', 'application/pdf', 10)`

// indexNames — имена индексов во всех узлах плана EXPLAIN (FORMAT JSON).
func indexNames(node any, out map[string]bool) {
	switch v := node.(type) {
	case map[string]any:
		if name, ok := v["Index Name"].(string); ok {
			out[name] = true
		}
		for _, child := range v {
			indexNames(child, out)
		}
	case []any:
		for _, child := range v {
			indexNames(child, out)
		}
	}
}

// Поиск предложений слова по урокам на истории размера замера ревью:
// у редкого слова план идёт по GIN-индексам хранимых лексем реплик и
// материалов (0022) и нигде не пересчитывает to_tsvector; результат —
// реплики уроков студента (урок слова первым, затем новые) и видимые
// участникам материалы, чужой урок и материал без загруженного файла не
// попадают.
func TestClozeLessonTextsUseIndexes(t *testing.T) {
	ctx := context.Background()
	s, err := New(ctx, testdb.DSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	started := time.Now()
	if _, err := s.pool.Exec(ctx, clozeHistorySQL); err != nil {
		t.Fatal(err)
	}
	// Как после autovacuum: список ожидания GIN влит в индекс, статистика собрана
	if _, err := s.pool.Exec(ctx, "VACUUM ANALYZE utterances, materials, transcripts, lessons, lesson_participants"); err != nil {
		t.Fatal(err)
	}
	t.Logf("история уроков заведена за %v", time.Since(started))
	var studentID int64
	if err := s.pool.QueryRow(ctx, `SELECT id FROM users WHERE email = 's@x'`).Scan(&studentID); err != nil {
		t.Fatal(err)
	}
	lesson := int64(7)

	// План — тот же, что у запроса слова: безымянный оператор с самим словом
	var plan string
	if err := s.pool.QueryRow(ctx, "EXPLAIN (FORMAT JSON) "+clozeWordTextsSQL, pgx.QueryExecModeExec,
		studentID, "'river'", lesson, clozeLessonTexts).Scan(&plan); err != nil {
		t.Fatal(err)
	}
	var tree any
	if err := json.Unmarshal([]byte(plan), &tree); err != nil {
		t.Fatal(err)
	}
	used := map[string]bool{}
	indexNames(tree, used)
	for _, idx := range []string{"utterances_text_tsv_idx", "materials_body_tsv_idx"} {
		if !used[idx] {
			t.Errorf("план не использует %s: %s", idx, plan)
		}
	}
	if strings.Contains(plan, "to_tsvector") {
		t.Errorf("план пересчитывает tsvector: %s", plan)
	}

	queries := []ClozeQuery{{Words: []string{"river"}, LessonID: &lesson}, {Words: []string{"go", "went", "gone", "going"}}}
	started = time.Now()
	texts, err := s.ClozeLessonTexts(ctx, studentID, queries)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("поиск по двум словам: %v", time.Since(started))
	var river []string
	for _, x := range texts[0] {
		river = append(river, x.Source+": "+x.Text)
	}
	want := []string{
		"lesson: We walked along the river in lesson 7.",
		"lesson: We walked along the river in lesson 3.",
		"material: The river is wide here.",
	}
	if !slices.Equal(river, want) {
		t.Fatalf("тексты river %q, хотели %q", river, want)
	}
	if len(texts[1]) != 2*clozeLessonTexts {
		t.Fatalf("текстов go: %d", len(texts[1]))
	}
}
