package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IEZhu/class/backend/internal/storage"
	"github.com/IEZhu/class/backend/internal/store"
)

// fakeStorage — хранилище в памяти вместо R2: подписанные ссылки — просто
// адреса с ключом, «загрузку» браузером тест делает методом upload.
type fakeStorage struct {
	mu      sync.Mutex
	objects map[string]storage.ObjectInfo
	puts    map[string]string // ключ → подписанный Content-Disposition
	deleted []string
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
	defer f.mu.Unlock()
	info, ok := f.objects[key]
	if !ok {
		return storage.ObjectInfo{}, storage.ErrObjectNotFound
	}
	return info, nil
}

func (f *fakeStorage) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objects, key)
	f.deleted = append(f.deleted, key)
	return nil
}

func (f *fakeStorage) upload(key string, size int64, contentType string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[key] = storage.ObjectInfo{Size: size, ContentType: contentType}
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

// keyOf — ключ, который api подписал под загрузку (в ответе его нет).
func keyOf(t *testing.T, resp createMaterialResponse) string {
	t.Helper()
	key, ok := strings.CutPrefix(resp.Upload.URL, "https://storage.example/put/")
	if !ok {
		t.Fatalf("ссылка не от хранилища: %q", resp.Upload.URL)
	}
	return key
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
		fileBody(strings.Repeat("я", maxFileNameRunes+1), "application/pdf", 10),
	} {
		expectStatus(t, securityRequest(fx.h, "POST", path, body, fx.teacher), http.StatusBadRequest)
	}
	expectStatus(t, securityRequest(fx.h, "POST", "/lessons/999999/materials", pdf, fx.teacher), http.StatusNotFound)
	if len(fx.fake.puts) != 0 {
		t.Fatalf("ссылки подписаны для отклонённых заявок: %v", fx.fake.puts)
	}

	// Путь из имени файла в ключ не попадает: ключ выбирает api
	resp := fx.requestUpload(t, `..\..\recordings/1/Конспект урока.pdf`, "Application/PDF; charset=binary", maxMaterialFileBytes)
	key := keyOf(t, resp)
	if !regexp.MustCompile(fmt.Sprintf(`^materials/%d/[0-9a-f]{32}\.pdf$`, fx.lessonID)).MatchString(key) {
		t.Fatalf("ключ %q не по раскладке materials/{lesson_id}/", key)
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
	var stored string
	if err := fx.a.store.Pool().QueryRow(context.Background(),
		`SELECT s3_key FROM materials WHERE id = $1`, resp.ID).Scan(&stored); err != nil || stored != key {
		t.Fatalf("в БД ключ %q, подписан %q: %v", stored, key, err)
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

	resp := fx.requestUpload(t, "slides.pptx", "application/vnd.openxmlformats-officedocument.presentationml.presentation", 4096)
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
	fx.fake.upload(key, maxMaterialFileBytes+1, "application/vnd.openxmlformats-officedocument.presentationml.presentation")
	expectStatus(t, securityRequest(fx.h, "POST", confirm, "", fx.teacher), http.StatusUnprocessableEntity)
	if fx.fake.has(key) {
		t.Fatal("отклонённый объект остался в хранилище")
	}
	expectStatus(t, securityRequest(fx.h, "POST", confirm, "", fx.teacher), http.StatusNotFound)

	// Подменённый тип при верном размере — тоже отказ
	resp = fx.requestUpload(t, "slides.pptx", "application/vnd.openxmlformats-officedocument.presentationml.presentation", 4096)
	key = keyOf(t, resp)
	fx.fake.upload(key, 4096, "text/html")
	expectStatus(t, securityRequest(fx.h, "POST", fmt.Sprintf("/materials/%d/confirm", resp.ID), "", fx.teacher), http.StatusUnprocessableEntity)

	// Честная загрузка: подтверждение идемпотентно
	resp = fx.requestUpload(t, "slides.pptx", "application/vnd.openxmlformats-officedocument.presentationml.presentation", 4096)
	key = keyOf(t, resp)
	confirm = fmt.Sprintf("/materials/%d/confirm", resp.ID)
	url = fmt.Sprintf("/materials/%d/url", resp.ID)
	fx.fake.upload(key, 4096, "application/vnd.openxmlformats-officedocument.presentationml.presentation")
	for range 2 {
		w := securityRequest(fx.h, "POST", confirm, "", fx.teacher)
		expectStatus(t, w, http.StatusOK)
		var m materialResponse
		if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil || m.File == nil || !m.File.Ready {
			t.Fatalf("файл не готов после подтверждения: %s", w.Body.String())
		}
	}
	if ms := fx.lessonMaterials(t, fx.student); len(ms) != 1 || ms[0].File == nil || !ms[0].File.Ready {
		t.Fatalf("участник не видит загруженный файл: %+v", ms)
	}

	// Скачивание — граница урока: преподаватель и участник да, чужие — 403
	for _, cookie := range []*http.Cookie{fx.teacher, fx.student} {
		w := securityRequest(fx.h, "GET", url, "", cookie)
		expectStatus(t, w, http.StatusOK)
		var got mediaURLResponse
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.URL != "https://storage.example/get/"+key {
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

	// Удаление: только преподаватель урока, объект уходит вместе со строкой
	del := fmt.Sprintf("/materials/%d", resp.ID)
	for _, cookie := range []*http.Cookie{fx.student, fx.otherTeacher} {
		expectStatus(t, securityRequest(fx.h, "DELETE", del, "", cookie), http.StatusForbidden)
	}
	expectStatus(t, securityRequest(fx.h, "DELETE", del, "", fx.teacher), http.StatusNoContent)
	if fx.fake.has(key) {
		t.Fatal("объект удалённого материала остался в хранилище")
	}
	expectStatus(t, securityRequest(fx.h, "GET", url, "", fx.student), http.StatusNotFound)
	expectStatus(t, securityRequest(fx.h, "DELETE", del, "", fx.teacher), http.StatusNotFound)
	expectStatus(t, securityRequest(fx.h, "DELETE", fmt.Sprintf("/materials/%d", text.ID), "", fx.teacher), http.StatusNoContent)

	// CHECK из 0013: ключ записи или чужого урока в материал не записать
	// даже в обход api
	for _, badKey := range []string{"recordings/1/room.mp4", fmt.Sprintf("materials/%d/x.pdf", fx.lessonID+1)} {
		_, err := fx.a.store.Pool().Exec(ctx, `INSERT INTO materials
			(lesson_id, kind, title, s3_key, file_name, file_content_type, file_size)
			VALUES ($1, 'material', 'x', $2, 'x.pdf', 'application/pdf', 1)`, fx.lessonID, badKey)
		if store.PgErrorCode(err) != pgCheckViolation {
			t.Fatalf("ключ %q: %v, хотели нарушение CHECK", badKey, err)
		}
	}

	// Отмена урока убирает и файлы его материалов: каскад до бакета не достаёт
	resp = fx.requestUpload(t, "song.mp3", "audio/mpeg", 300)
	key = keyOf(t, resp)
	fx.fake.upload(key, 300, "audio/mpeg")
	expectStatus(t, securityRequest(fx.h, "POST", fmt.Sprintf("/materials/%d/confirm", resp.ID), "", fx.teacher), http.StatusOK)
	expectStatus(t, securityRequest(fx.h, "DELETE", fmt.Sprintf("/lessons/%d", fx.lessonID), "", fx.teacher), http.StatusNoContent)
	if fx.fake.has(key) {
		t.Fatal("файл отменённого урока остался в хранилище")
	}
}
