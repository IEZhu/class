package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/IEZhu/class/backend/internal/jobs"
	"github.com/IEZhu/class/backend/internal/storage"
	"github.com/IEZhu/class/backend/internal/store"
	"github.com/IEZhu/class/backend/internal/worker"
)

// fakeStorage — хранилище в памяти вместо R2: подписанные ссылки — просто
// адреса с ключом, «загрузку» браузером тест делает методом upload.
type fakeStorage struct {
	mu      sync.Mutex
	objects map[string]storage.ObjectInfo
	puts    map[string]string // ключ → подписанный Content-Disposition
	deleted []string
	writes  int // каждая запись объекта даёт новый ETag, как у S3
	// failDeletes — столько ближайших Delete ответят ошибкой хранилища
	failDeletes int
	// onStat зовётся перед Stat: так тест вклинивается между шагами confirm
	onStat func(key string)
}

func newFakeStorage() *fakeStorage {
	return &fakeStorage{objects: map[string]storage.ObjectInfo{}, puts: map[string]string{}}
}

func (f *fakeStorage) PresignGet(_ context.Context, key string, _ time.Duration) (string, error) {
	return "https://storage.example/get/" + key, nil
}

func (f *fakeStorage) PresignPut(_ context.Context, key, contentType, disposition string, _ int64, _ time.Duration) (storage.PresignedPut, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts[key] = disposition
	h := http.Header{}
	h.Set("Content-Type", contentType)
	h.Set("Content-Disposition", disposition)
	return storage.PresignedPut{URL: "https://storage.example/put/" + key, Header: h}, nil
}

func (f *fakeStorage) Stat(_ context.Context, key string) (storage.ObjectInfo, error) {
	f.mu.Lock()
	hook := f.onStat
	f.mu.Unlock()
	if hook != nil {
		hook(key)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	info, ok := f.objects[key]
	if !ok {
		return storage.ObjectInfo{}, storage.ErrObjectNotFound
	}
	return info, nil
}

// Copy, как CopyObject у S3: копия однократной загрузки с тем же ETag.
func (f *fakeStorage) Copy(_ context.Context, src, dst string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	info, ok := f.objects[src]
	if !ok {
		return fmt.Errorf("copy %s: NoSuchKey", src)
	}
	f.objects[dst] = info
	return nil
}

func (f *fakeStorage) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failDeletes > 0 {
		f.failDeletes--
		return errors.New("delete " + key + ": 503 SlowDown")
	}
	delete(f.objects, key)
	f.deleted = append(f.deleted, key)
	return nil
}

func (f *fakeStorage) upload(key string, size int64, contentType string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes++
	f.objects[key] = storage.ObjectInfo{Size: size, ContentType: contentType, ETag: fmt.Sprintf(`"etag-%d"`, f.writes)}
}

func (f *fakeStorage) has(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.objects[key]
	return ok
}

// materialFixture — урок группы: преподаватель, участник, чужие студент
// и преподаватель.
type materialFixture struct {
	a                                        *API
	h                                        http.Handler
	fake                                     *fakeStorage
	lessonID                                 int64
	teacher, otherTeacher, student, outsider *http.Cookie
}

func newMaterialFixture(t *testing.T) *materialFixture {
	t.Helper()
	a, conn := securityDB(t)
	ctx := context.Background()
	teacherID, teacher := securityUser(t, a, conn, store.RoleTeacher, "files-teacher")
	_, otherTeacher := securityUser(t, a, conn, store.RoleTeacher, "files-other-teacher")
	studentID, student := securityUser(t, a, conn, store.RoleStudent, "files-student")
	_, outsider := securityUser(t, a, conn, store.RoleStudent, "files-outsider")
	var groupID int64
	if err := conn.QueryRow(ctx, `INSERT INTO groups(name,level) VALUES ('files','B1') RETURNING id`).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO group_members(group_id,user_id) VALUES($1,$2)`, groupID, studentID); err != nil {
		t.Fatal(err)
	}
	lesson, err := a.store.CreateLesson(ctx, &groupID, "", teacherID, time.Now().Add(time.Hour), time.Now().Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return &materialFixture{a: a, h: a.Router(), lessonID: lesson.ID,
		teacher: teacher, otherTeacher: otherTeacher, student: student, outsider: outsider}
}

func (fx *materialFixture) withStorage() {
	fx.fake = newFakeStorage()
	fx.a.storage = fx.fake
}

func fileBody(name, contentType string, size int64) string {
	b, _ := json.Marshal(map[string]any{"title": "", "file": map[string]any{
		"name": name, "content_type": contentType, "size": size}})
	return string(b)
}

// requestUpload заводит материал с файлом и возвращает ответ api.
func (fx *materialFixture) requestUpload(t *testing.T, name, contentType string, size int64) createMaterialResponse {
	t.Helper()
	w := securityRequest(fx.h, "POST", fmt.Sprintf("/lessons/%d/materials", fx.lessonID), fileBody(name, contentType, size), fx.teacher)
	expectStatus(t, w, http.StatusCreated)
	var resp createMaterialResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Upload == nil || resp.File == nil || resp.File.Ready {
		t.Fatalf("нет ссылки на загрузку или файл уже готов: %s", w.Body.String())
	}
	return resp
}

// keyOf — ключ, который api подписал под загрузку (в ответе его нет):
// копия под materials-pending/.
func keyOf(t *testing.T, resp createMaterialResponse) string {
	t.Helper()
	key, ok := strings.CutPrefix(resp.Upload.URL, "https://storage.example/put/")
	if !ok {
		t.Fatalf("ссылка не от хранилища: %q", resp.Upload.URL)
	}
	return key
}

// storedKey — постоянный ключ файла из строки материала.
func (fx *materialFixture) storedKey(t *testing.T, id int64) string {
	t.Helper()
	var key string
	if err := fx.a.store.Pool().QueryRow(context.Background(),
		`SELECT s3_key FROM materials WHERE id = $1`, id).Scan(&key); err != nil {
		t.Fatal(err)
	}
	return key
}

// uploadAndConfirm — честная загрузка и подтверждение; постоянный ключ.
func (fx *materialFixture) uploadAndConfirm(t *testing.T, name, contentType string, size int64) (createMaterialResponse, string) {
	t.Helper()
	resp := fx.requestUpload(t, name, contentType, size)
	fx.fake.upload(keyOf(t, resp), size, contentType)
	expectStatus(t, securityRequest(fx.h, "POST", fmt.Sprintf("/materials/%d/confirm", resp.ID), "", fx.teacher), http.StatusOK)
	return resp, fx.storedKey(t, resp.ID)
}

func materialExists(t *testing.T, fx *materialFixture, id int64) bool {
	t.Helper()
	var n int
	if err := fx.a.store.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM materials WHERE id = $1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func (fx *materialFixture) lessonMaterials(t *testing.T, cookie *http.Cookie) []materialResponse {
	t.Helper()
	w := securityRequest(fx.h, "GET", fmt.Sprintf("/lessons/%d", fx.lessonID), "", cookie)
	expectStatus(t, w, http.StatusOK)
	var d struct {
		Materials []materialResponse `json:"materials"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	return d.Materials
}

func TestMaterialFileUploadLimitsAndAuthorization(t *testing.T) {
	fx := newMaterialFixture(t)
	path := fmt.Sprintf("/lessons/%d/materials", fx.lessonID)
	pdf := fileBody("notes.pdf", "application/pdf", 1000)

	// Без хранилища файл не завести, а текстовый материал — как раньше
	expectStatus(t, securityRequest(fx.h, "POST", path, pdf, fx.teacher), http.StatusServiceUnavailable)
	expectStatus(t, securityRequest(fx.h, "POST", path, `{"title":"текст","body_md":"x"}`, fx.teacher), http.StatusCreated)

	fx.withStorage()
	expectStatus(t, securityRequest(fx.h, "POST", path, pdf, nil), http.StatusUnauthorized)
	for _, cookie := range []*http.Cookie{fx.student, fx.outsider, fx.otherTeacher} {
		expectStatus(t, securityRequest(fx.h, "POST", path, pdf, cookie), http.StatusForbidden)
		expectStatus(t, securityRequest(fx.h, "POST", fmt.Sprintf("/lessons/%d/homework", fx.lessonID), pdf, cookie), http.StatusForbidden)
	}
	for _, body := range []string{
		fileBody("page.html", "text/html", 1000),
		fileBody("run.sh", "application/x-sh", 10),
		fileBody("notes.pdf", "not a type", 1000),
		fileBody("big.mp4", "video/mp4", maxMaterialFileBytes+1),
		fileBody("empty.pdf", "application/pdf", 0),
		fileBody("negative.pdf", "application/pdf", -5),
		fileBody("", "application/pdf", 10),
		fileBody("dir/", "application/pdf", 10),
		fileBody("bad\x00name.pdf", "application/pdf", 10),
		// Невидимые символы форматирования (Cf): смена направления текста
		// показала бы «homeworkexe.pdf», а сохранился бы .exe
		fileBody("homework\u202efdp.exe", "application/pdf", 10),
		fileBody("zero\u200bwidth.pdf", "application/pdf", 10),
		fileBody(strings.Repeat("я", maxFileNameRunes+1), "application/pdf", 10),
	} {
		expectStatus(t, securityRequest(fx.h, "POST", path, body, fx.teacher), http.StatusBadRequest)
	}
	expectStatus(t, securityRequest(fx.h, "POST", "/lessons/999999/materials", pdf, fx.teacher), http.StatusNotFound)
	if len(fx.fake.puts) != 0 {
		t.Fatalf("ссылки подписаны для отклонённых заявок: %v", fx.fake.puts)
	}

	// Путь из имени файла в ключ не попадает: ключ выбирает api. Браузер
	// грузит под materials-pending/, постоянный ключ — под materials/
	resp := fx.requestUpload(t, `..\..\recordings/1/Конспект урока.pdf`, "Application/PDF; charset=binary", maxMaterialFileBytes)
	key := keyOf(t, resp)
	if !regexp.MustCompile(fmt.Sprintf(`^materials-pending/%d/[0-9a-f]{32}\.pdf$`, fx.lessonID)).MatchString(key) {
		t.Fatalf("ключ загрузки %q не по раскладке materials-pending/{lesson_id}/", key)
	}
	if resp.File.Name != "Конспект урока.pdf" || resp.Title != "Конспект урока.pdf" ||
		resp.File.ContentType != "application/pdf" || resp.File.Size != maxMaterialFileBytes {
		t.Fatalf("метаданные файла: %+v, title %q", resp.File, resp.Title)
	}
	if resp.Upload.Method != http.MethodPut || resp.Upload.Headers["Content-Type"] != "application/pdf" {
		t.Fatalf("загрузка: %+v", resp.Upload)
	}
	// Имя для скачивания — в подписанном Content-Disposition (RFC 2231)
	if d := resp.Upload.Headers["Content-Disposition"]; !strings.HasPrefix(d, "attachment;") ||
		!strings.Contains(d, "%D0%9A%D0%BE%D0%BD%D1%81%D0%BF%D0%B5%D0%BA%D1%82") {
		t.Fatalf("Content-Disposition = %q", d)
	}
	if stored := fx.storedKey(t, resp.ID); storage.PendingMaterialKey(stored) != key ||
		!strings.HasPrefix(stored, fmt.Sprintf("materials/%d/", fx.lessonID)) {
		t.Fatalf("в БД ключ %q, подписана загрузка %q", stored, key)
	}
	// Домашка с файлом идёт тем же путём
	w := securityRequest(fx.h, "POST", fmt.Sprintf("/lessons/%d/homework", fx.lessonID),
		`{"title":"Аудио к заданию","file":{"name":"track.m4a","content_type":"audio/x-m4a","size":2048}}`, fx.teacher)
	expectStatus(t, w, http.StatusCreated)
}

func TestMaterialFileConfirmDownloadDelete(t *testing.T) {
	fx := newMaterialFixture(t)
	fx.withStorage()
	ctx := context.Background()
	const pptx = "application/vnd.openxmlformats-officedocument.presentationml.presentation"

	resp := fx.requestUpload(t, "slides.pptx", pptx, 4096)
	key := keyOf(t, resp)
	confirm := fmt.Sprintf("/materials/%d/confirm", resp.ID)
	url := fmt.Sprintf("/materials/%d/url", resp.ID)

	// Незавершённую загрузку видит только преподаватель
	if ms := fx.lessonMaterials(t, fx.student); len(ms) != 0 {
		t.Fatalf("участник видит незагруженный файл: %+v", ms)
	}
	if ms := fx.lessonMaterials(t, fx.teacher); len(ms) != 1 || ms[0].File == nil || ms[0].File.Ready {
		t.Fatalf("преподаватель не видит ожидающий файл: %+v", ms)
	}

	// Подтверждает только преподаватель урока; объекта нет — 409, строка цела
	expectStatus(t, securityRequest(fx.h, "POST", confirm, "", fx.student), http.StatusForbidden)
	expectStatus(t, securityRequest(fx.h, "POST", confirm, "", fx.otherTeacher), http.StatusForbidden)
	expectStatus(t, securityRequest(fx.h, "POST", confirm, "", fx.teacher), http.StatusConflict)
	expectStatus(t, securityRequest(fx.h, "GET", url, "", fx.teacher), http.StatusConflict)

	// Загружено больше заявленного (и сверх лимита) — объект и материал удалены
	fx.fake.upload(key, maxMaterialFileBytes+1, pptx)
	expectStatus(t, securityRequest(fx.h, "POST", confirm, "", fx.teacher), http.StatusUnprocessableEntity)
	if fx.fake.has(key) {
		t.Fatal("отклонённый объект остался в хранилище")
	}
	expectStatus(t, securityRequest(fx.h, "POST", confirm, "", fx.teacher), http.StatusNotFound)

	// Подменённый тип при верном размере — тоже отказ
	resp = fx.requestUpload(t, "slides.pptx", pptx, 4096)
	fx.fake.upload(keyOf(t, resp), 4096, "text/html")
	expectStatus(t, securityRequest(fx.h, "POST", fmt.Sprintf("/materials/%d/confirm", resp.ID), "", fx.teacher), http.StatusUnprocessableEntity)

	// Меньше заявленного, но в пределах лимита — отказ по равенству размера:
	// обрезанная загрузка не станет готовым файлом
	resp = fx.requestUpload(t, "slides.pptx", pptx, 4096)
	key = keyOf(t, resp)
	fx.fake.upload(key, 4095, pptx)
	expectStatus(t, securityRequest(fx.h, "POST", fmt.Sprintf("/materials/%d/confirm", resp.ID), "", fx.teacher), http.StatusUnprocessableEntity)
	if fx.fake.has(key) || materialExists(t, fx, resp.ID) {
		t.Fatal("урезанная загрузка или её материал остались")
	}

	// Честная загрузка: confirm переносит её на постоянный ключ внутри
	// хранилища и убирает копию загрузки; подтверждение идемпотентно
	resp = fx.requestUpload(t, "slides.pptx", pptx, 4096)
	key = keyOf(t, resp)
	final := fx.storedKey(t, resp.ID)
	confirm = fmt.Sprintf("/materials/%d/confirm", resp.ID)
	url = fmt.Sprintf("/materials/%d/url", resp.ID)
	fx.fake.upload(key, 4096, pptx)
	for range 2 {
		w := securityRequest(fx.h, "POST", confirm, "", fx.teacher)
		expectStatus(t, w, http.StatusOK)
		var m materialResponse
		if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil || m.File == nil || !m.File.Ready {
			t.Fatalf("файл не готов после подтверждения: %s", w.Body.String())
		}
	}
	if !fx.fake.has(final) || fx.fake.has(key) {
		t.Fatalf("после подтверждения: постоянный %v, загрузка %v — хотели только постоянный", fx.fake.has(final), fx.fake.has(key))
	}
	var etag string
	if err := fx.a.store.Pool().QueryRow(ctx, `SELECT file_etag FROM materials WHERE id = $1`, resp.ID).Scan(&etag); err != nil || etag == "" {
		t.Fatalf("ETag подтверждённого файла не записан: %q %v", etag, err)
	}
	if ms := fx.lessonMaterials(t, fx.student); len(ms) != 1 || ms[0].File == nil || !ms[0].File.Ready {
		t.Fatalf("участник не видит загруженный файл: %+v", ms)
	}

	// Скачивание — граница урока: преподаватель и участник да, чужие — 403
	for _, cookie := range []*http.Cookie{fx.teacher, fx.student} {
		w := securityRequest(fx.h, "GET", url, "", cookie)
		expectStatus(t, w, http.StatusOK)
		var got mediaURLResponse
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.URL != "https://storage.example/get/"+final {
			t.Fatalf("ссылка на скачивание: %s", w.Body.String())
		}
	}
	for _, cookie := range []*http.Cookie{fx.outsider, fx.otherTeacher} {
		expectStatus(t, securityRequest(fx.h, "GET", url, "", cookie), http.StatusForbidden)
	}
	expectStatus(t, securityRequest(fx.h, "GET", url, "", nil), http.StatusUnauthorized)
	expectStatus(t, securityRequest(fx.h, "GET", "/materials/999999/url", "", fx.student), http.StatusNotFound)

	// У текстового материала файла нет
	w := securityRequest(fx.h, "POST", fmt.Sprintf("/lessons/%d/materials", fx.lessonID), `{"title":"текст"}`, fx.teacher)
	expectStatus(t, w, http.StatusCreated)
	var text materialResponse
	if err := json.Unmarshal(w.Body.Bytes(), &text); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, securityRequest(fx.h, "GET", fmt.Sprintf("/materials/%d/url", text.ID), "", fx.student), http.StatusNotFound)
	expectStatus(t, securityRequest(fx.h, "POST", fmt.Sprintf("/materials/%d/confirm", text.ID), "", fx.teacher), http.StatusConflict)

	// Удаление: только преподаватель урока, объекты уходят вместе со строкой
	del := fmt.Sprintf("/materials/%d", resp.ID)
	for _, cookie := range []*http.Cookie{fx.student, fx.otherTeacher} {
		expectStatus(t, securityRequest(fx.h, "DELETE", del, "", cookie), http.StatusForbidden)
	}
	fx.fake.upload(key, 4096, pptx) // догрузка по ещё живой ссылке
	expectStatus(t, securityRequest(fx.h, "DELETE", del, "", fx.teacher), http.StatusNoContent)
	if fx.fake.has(final) || fx.fake.has(key) {
		t.Fatal("объект удалённого материала остался в хранилище")
	}
	expectStatus(t, securityRequest(fx.h, "GET", url, "", fx.student), http.StatusNotFound)
	expectStatus(t, securityRequest(fx.h, "DELETE", del, "", fx.teacher), http.StatusNotFound)
	expectStatus(t, securityRequest(fx.h, "DELETE", fmt.Sprintf("/materials/%d", text.ID), "", fx.teacher), http.StatusNoContent)
}

// Ключом хранилища владеет и LiveKit Egress (ADR-014): подтверждённый файл,
// перезаписанный в обход api, участникам не отдаётся.
func TestMaterialFileChangedAfterConfirm(t *testing.T) {
	fx := newMaterialFixture(t)
	fx.withStorage()
	url := func(id int64) string { return fmt.Sprintf("/materials/%d/url", id) }

	// Новые байты того же типа и длины — другой ETag
	resp, final := fx.uploadAndConfirm(t, "notes.pdf", "application/pdf", 1000)
	expectStatus(t, securityRequest(fx.h, "GET", url(resp.ID), "", fx.student), http.StatusOK)
	fx.fake.upload(final, 1000, "application/pdf")
	w := securityRequest(fx.h, "GET", url(resp.ID), "", fx.student)
	expectStatus(t, w, http.StatusConflict)
	if !strings.Contains(w.Body.String(), "file_changed") {
		t.Fatalf("ответ на подменённый файл: %s", w.Body.String())
	}

	// Смена метаданных копированием на себя ETag не меняет — ловит сверка
	// типа и размера
	for _, tampered := range []func(storage.ObjectInfo) storage.ObjectInfo{
		func(i storage.ObjectInfo) storage.ObjectInfo { i.ContentType = "text/html"; return i },
		func(i storage.ObjectInfo) storage.ObjectInfo { i.Size++; return i },
	} {
		resp, final := fx.uploadAndConfirm(t, "notes.pdf", "application/pdf", 1000)
		fx.fake.mu.Lock()
		fx.fake.objects[final] = tampered(fx.fake.objects[final])
		fx.fake.mu.Unlock()
		expectStatus(t, securityRequest(fx.h, "GET", url(resp.ID), "", fx.student), http.StatusConflict)
	}

	// Объект пропал — тоже 409, ссылки на пустоту нет
	resp, final = fx.uploadAndConfirm(t, "notes.pdf", "application/pdf", 1000)
	fx.fake.mu.Lock()
	delete(fx.fake.objects, final)
	fx.fake.mu.Unlock()
	expectStatus(t, securityRequest(fx.h, "GET", url(resp.ID), "", fx.student), http.StatusConflict)
}

// Имя для скачивания несёт расширение заявленного типа: тип по белому
// списку — не просто подпись, под которой сохранится setup.exe.
func TestMaterialFileDownloadName(t *testing.T) {
	fx := newMaterialFixture(t)
	fx.withStorage()
	for _, c := range []struct{ name, contentType, want string }{
		{"setup.exe", "application/pdf", "setup.pdf"},
		{"page.html", "text/plain", "page.txt"},
		{"notes", "text/plain", "notes.txt"},
		{"конспект.", "application/pdf", "конспект.pdf"},
		{"Урок.pdf", "application/pdf", "Урок.pdf"},
		{"Отчёт.PDF", "application/pdf", "Отчёт.PDF"},
		{"photo.JPEG", "image/jpeg", "photo.JPEG"},
		{"photo.jpg", "image/jpeg", "photo.jpg"},
		{"voice.m4a", "audio/x-m4a", "voice.m4a"},
	} {
		resp := fx.requestUpload(t, c.name, c.contentType, 10)
		if resp.File.Name != c.want {
			t.Errorf("%q (%s): имя %q, хотели %q", c.name, c.contentType, resp.File.Name, c.want)
		}
		if d := fx.fake.puts[keyOf(t, resp)]; d != mime.FormatMediaType("attachment", map[string]string{"filename": c.want}) {
			t.Errorf("%q: Content-Disposition %q", c.name, d)
		}
	}
	// Имя, которое расширение вывело за предел, — 400 с понятным текстом
	w := securityRequest(fx.h, "POST", fmt.Sprintf("/lessons/%d/materials", fx.lessonID),
		fileBody(strings.Repeat("я", maxFileNameRunes-1), "application/pdf", 10), fx.teacher)
	expectStatus(t, w, http.StatusBadRequest)
	if !strings.Contains(w.Body.String(), "до 200 символов") {
		t.Errorf("текст ошибки длины: %s", w.Body.String())
	}
	w = securityRequest(fx.h, "POST", fmt.Sprintf("/lessons/%d/materials", fx.lessonID),
		fileBody("homework\u202efdp.exe", "application/pdf", 10), fx.teacher)
	expectStatus(t, w, http.StatusBadRequest)
	if !strings.Contains(w.Body.String(), "невидимые") {
		t.Errorf("текст ошибки символов: %s", w.Body.String())
	}
}

// Пограничные случаи подтверждения: charset у text/plain, гонка отказа
// с удалением, хранилище, пропавшее из конфигурации.
func TestMaterialFileConfirmEdgeCases(t *testing.T) {
	fx := newMaterialFixture(t)
	fx.withStorage()

	// Хранилище вправе дописать charset — тип тот же, файл принят
	resp := fx.requestUpload(t, "words.txt", "text/plain", 64)
	fx.fake.upload(keyOf(t, resp), 64, "text/plain; charset=utf-8")
	expectStatus(t, securityRequest(fx.h, "POST", fmt.Sprintf("/materials/%d/confirm", resp.ID), "", fx.teacher), http.StatusOK)

	// Загрузка не та, а материал тем временем удалил параллельный DELETE:
	// отвергать уже нечего — 404, а не 500
	resp = fx.requestUpload(t, "a.pdf", "application/pdf", 1000)
	fx.fake.upload(keyOf(t, resp), 999, "application/pdf")
	fx.fake.onStat = func(string) { _ = fx.a.store.DeleteMaterial(context.Background(), resp.ID, nil) }
	expectStatus(t, securityRequest(fx.h, "POST", fmt.Sprintf("/materials/%d/confirm", resp.ID), "", fx.teacher), http.StatusNotFound)
	fx.fake.onStat = nil

	// Ключи хранилища убрали из .env, а файлы уже есть: 503, строка цела
	resp, _ = fx.uploadAndConfirm(t, "b.pdf", "application/pdf", 100)
	pending := fx.requestUpload(t, "c.pdf", "application/pdf", 100)
	fx.a.storage = nil
	expectStatus(t, securityRequest(fx.h, "GET", fmt.Sprintf("/materials/%d/url", resp.ID), "", fx.student), http.StatusServiceUnavailable)
	expectStatus(t, securityRequest(fx.h, "POST", fmt.Sprintf("/materials/%d/confirm", pending.ID), "", fx.teacher), http.StatusServiceUnavailable)
	for _, id := range []int64{resp.ID, pending.ID} {
		expectStatus(t, securityRequest(fx.h, "DELETE", fmt.Sprintf("/materials/%d", id), "", fx.teacher), http.StatusServiceUnavailable)
		if !materialExists(t, fx, id) {
			t.Fatalf("материал %d удалён без хранилища", id)
		}
	}
}

// CHECK из 0013 держит схему и в обход api: ключ записи или чужого урока,
// ключ без метаданных (NULL в сравнении CHECK пропустил бы), отметка
// подтверждения без ETag.
func TestMaterialFileSchemaChecks(t *testing.T) {
	fx := newMaterialFixture(t)
	ctx := context.Background()
	own := fmt.Sprintf("materials/%d/x.pdf", fx.lessonID)
	for name, insert := range map[string]func() error{
		"ключ записи": func() error {
			_, err := fx.a.store.Pool().Exec(ctx, `INSERT INTO materials (lesson_id, kind, title, s3_key, file_name, file_content_type, file_size)
				VALUES ($1, 'material', 'x', 'recordings/1/room.mp4', 'x.pdf', 'application/pdf', 1)`, fx.lessonID)
			return err
		},
		"ключ чужого урока": func() error {
			_, err := fx.a.store.Pool().Exec(ctx, `INSERT INTO materials (lesson_id, kind, title, s3_key, file_name, file_content_type, file_size)
				VALUES ($1, 'material', 'x', $2, 'x.pdf', 'application/pdf', 1)`, fx.lessonID, fmt.Sprintf("materials/%d/x.pdf", fx.lessonID+1))
			return err
		},
		"ключ без метаданных": func() error {
			_, err := fx.a.store.Pool().Exec(ctx, `INSERT INTO materials (lesson_id, kind, title, s3_key)
				VALUES ($1, 'material', 'x', $2)`, fx.lessonID, own)
			return err
		},
		"подтверждение без ETag": func() error {
			_, err := fx.a.store.Pool().Exec(ctx, `INSERT INTO materials (lesson_id, kind, title, s3_key, file_name, file_content_type, file_size, file_uploaded_at)
				VALUES ($1, 'material', 'x', $2, 'x.pdf', 'application/pdf', 1, now())`, fx.lessonID, own)
			return err
		},
		"ETag без файла": func() error {
			_, err := fx.a.store.Pool().Exec(ctx, `INSERT INTO materials (lesson_id, kind, title, file_etag)
				VALUES ($1, 'material', 'x', '"e"')`, fx.lessonID)
			return err
		},
	} {
		if err := insert(); store.PgErrorCode(err) != pgCheckViolation {
			t.Errorf("%s: %v, хотели нарушение CHECK", name, err)
		}
	}
}

// Объекты, на которые строк больше нет, убирает worker джобой
// material_files_delete: она ставится той же транзакцией, что удаляет
// строки, и повторяется, пока хранилище не ответит (ADR-024).
func TestMaterialFileCleanupJobs(t *testing.T) {
	fx := newMaterialFixture(t)
	fx.withStorage()
	ctx := context.Background()
	pool := fx.a.store.Pool()
	queued := func() [][]string {
		t.Helper()
		rows, err := pool.Query(ctx, `SELECT payload FROM jobs WHERE kind = $1 ORDER BY id`, store.JobMaterialFilesDelete)
		if err != nil {
			t.Fatal(err)
		}
		payloads, err := pgx.CollectRows(rows, pgx.RowTo[store.MaterialFilesDelete])
		if err != nil {
			t.Fatal(err)
		}
		out := [][]string{}
		for _, p := range payloads {
			out = append(out, p.Keys)
		}
		return out
	}
	runner := jobs.NewRunner(pool, jobs.Config{WorkerID: "test-worker", MaxAttempts: 3})
	worker.RegisterMaterialFiles(runner, fx.fake)
	runJob := func() {
		t.Helper()
		if worked, err := runner.RunOnce(ctx); err != nil || !worked {
			t.Fatalf("RunOnce = %v, %v", worked, err)
		}
	}

	// Хранилище не ответило на DELETE /materials/{id}: строка удалена,
	// ключ — в джобе
	resp, final := fx.uploadAndConfirm(t, "a.pdf", "application/pdf", 10)
	fx.fake.failDeletes = 1
	expectStatus(t, securityRequest(fx.h, "DELETE", fmt.Sprintf("/materials/%d", resp.ID), "", fx.teacher), http.StatusNoContent)
	if materialExists(t, fx, resp.ID) || !fx.fake.has(final) {
		t.Fatal("строка должна уйти, объект — остаться для worker'а")
	}
	if got := queued(); len(got) != 1 || len(got[0]) != 1 || got[0][0] != final {
		t.Fatalf("джобы после сбоя удаления: %v", got)
	}
	runJob()
	if fx.fake.has(final) || len(queued()) != 0 {
		t.Fatal("worker не убрал объект удалённого материала")
	}

	// Отмена урока: api объекты не трогает, ключи уходят в джобу вместе
	// с удалением урока. Загрузка, догнавшая отмену, — под тем же ключом
	// в materials-pending/, её джоба убирает тоже
	ready, readyKey := fx.uploadAndConfirm(t, "b.mp3", "audio/mpeg", 300)
	waiting := fx.requestUpload(t, "c.pdf", "application/pdf", 50)
	waitingKey := fx.storedKey(t, waiting.ID)
	expectStatus(t, securityRequest(fx.h, "DELETE", fmt.Sprintf("/lessons/%d", fx.lessonID), "", fx.teacher), http.StatusNoContent)
	fx.fake.upload(keyOf(t, waiting), 50, "application/pdf")
	if materialExists(t, fx, ready.ID) || !fx.fake.has(readyKey) {
		t.Fatal("отмена урока должна удалить строки и оставить объекты worker'у")
	}
	got := queued()
	if len(got) != 1 || !slices.Equal(slices.Sorted(slices.Values(got[0])), slices.Sorted(slices.Values([]string{readyKey, waitingKey}))) {
		t.Fatalf("джоба отмены: %v, хотели ключи %s и %s", got, readyKey, waitingKey)
	}

	// Сбой хранилища — повтор с backoff; следующая попытка доделывает
	fx.fake.failDeletes = 1
	runJob()
	var attempts int
	var lastError string
	if err := pool.QueryRow(ctx, `SELECT attempts, last_error FROM jobs WHERE kind = $1`,
		store.JobMaterialFilesDelete).Scan(&attempts, &lastError); err != nil || attempts != 1 || !strings.Contains(lastError, "SlowDown") {
		t.Fatalf("после сбоя: попыток %d, ошибка %q (%v)", attempts, lastError, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE jobs SET run_at = now()`); err != nil {
		t.Fatal(err)
	}
	runJob()
	for _, key := range []string{readyKey, waitingKey, keyOf(t, waiting)} {
		if fx.fake.has(key) {
			t.Errorf("объект %s отменённого урока остался", key)
		}
	}
	if n := len(queued()); n != 0 {
		t.Fatalf("джоба не снята после успеха: %d", n)
	}
}
