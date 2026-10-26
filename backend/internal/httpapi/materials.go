package httpapi

import (
	"context"
	"errors"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/IEZhu/class/backend/internal/storage"
	"github.com/IEZhu/class/backend/internal/store"
)

// Ограничения на файлы материалов (ADR-024). api проверяет заявку при
// выдаче ссылки на загрузку и ещё раз — сам объект при подтверждении.
const (
	maxMaterialFileBytes = 50 << 20 // 50 MiB: PDF, презентация, аудио к уроку
	maxFileNameRunes     = 200
	// uploadURLTTL короче ссылки на скачивание: загрузку браузер начинает
	// сразу после выдачи, а ссылка на запись в бакет — не та, что стоит
	// держать живой
	uploadURLTTL = 10 * time.Minute
)

// materialFileTypes — допустимые типы файлов и расширение ключа в бакете.
// Остальное (HTML, скрипты, архивы) не принимается.
var materialFileTypes = map[string]string{
	"application/pdf": "pdf",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   "docx",
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": "pptx",
	"text/plain": "txt",
	"image/png":  "png",
	"image/jpeg": "jpg",
	"image/webp": "webp",
	"audio/mpeg": "mp3",
	"audio/mp4":  "m4a",
	// так m4a подписывают Chrome и Safari на macOS
	"audio/x-m4a": "m4a",
	"audio/ogg":   "ogg",
	"video/mp4":   "mp4",
}

type materialResponse struct {
	ID        int64     `json:"id"`
	LessonID  int64     `json:"lesson_id"`
	Kind      string    `json:"kind"`
	Title     string    `json:"title"`
	BodyMD    string    `json:"body_md"`
	CreatedAt time.Time `json:"created_at"`
	// null у текстового материала; ключ в бакете наружу не отдаётся
	File *materialFileResponse `json:"file"`
}

type materialFileResponse struct {
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	// false — загрузка не подтверждена, файл видит только преподаватель
	Ready bool `json:"ready"`
}

type uploadResponse struct {
	URL       string            `json:"url"`
	Method    string            `json:"method"`
	Headers   map[string]string `json:"headers"`
	ExpiresAt time.Time         `json:"expires_at"`
}

type createMaterialResponse struct {
	materialResponse
	// Только у материала с файлом: куда и с какими заголовками браузеру
	// грузить файл, после чего — POST /materials/{id}/confirm
	Upload *uploadResponse `json:"upload,omitempty"`
}

func toMaterialResponse(m store.Material) materialResponse {
	resp := materialResponse{
		ID: m.ID, LessonID: m.LessonID, Kind: m.Kind,
		Title: m.Title, BodyMD: m.BodyMD, CreatedAt: m.CreatedAt,
	}
	if m.File != nil {
		resp.File = &materialFileResponse{
			Name: m.File.Name, ContentType: m.File.ContentType, Size: m.File.Size, Ready: m.File.Ready(),
		}
	}
	return resp
}

func toMaterialResponses(ms []store.Material) []materialResponse {
	out := make([]materialResponse, 0, len(ms))
	for _, m := range ms {
		out = append(out, toMaterialResponse(m))
	}
	return out
}

// visibleMaterials — что из материалов видит пользователь: незавершённую
// загрузку — только преподаватель урока, участникам показывать нечего.
func visibleMaterials(ms []store.Material, isTeacher bool) []store.Material {
	if isTeacher {
		return ms
	}
	out := make([]store.Material, 0, len(ms))
	for _, m := range ms {
		if m.File == nil || m.File.Ready() {
			out = append(out, m)
		}
	}
	return out
}

type fileRequest struct {
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
}

// handleCreateMaterial обслуживает POST /lessons/{id}/materials и /homework:
// kind определяется маршрутом. С полем file материал заводится вместе
// с файлом, ожидающим загрузки, и в ответе приходит presigned PUT.
func (a *API) handleCreateMaterial(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lessonID, ok := pathID(w, r)
		if !ok {
			return
		}
		var req struct {
			Title  string       `json:"title"`
			BodyMD string       `json:"body_md"`
			File   *fileRequest `json:"file"`
		}
		if err := decodeBody(w, r, &req); err != nil {
			badBody(w, err, "невалидный JSON")
			return
		}
		title := req.Title
		var file *store.MaterialFile
		if req.File != nil {
			var msg string
			if file, msg = validFile(*req.File); file == nil {
				writeError(w, http.StatusBadRequest, "bad_request", msg)
				return
			}
			// Без названия материал подписывается именем файла
			if strings.TrimSpace(title) == "" {
				title = file.Name
			}
		}
		if title == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "title обязателен (body_md — markdown, опционален)")
			return
		}
		// материал вправе добавлять только teacher этого урока
		teacherID, err := a.store.LessonTeacherID(r.Context(), lessonID)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "урок не найден")
			return
		}
		if err != nil {
			internalError(w, "load lesson for material", err)
			return
		}
		if userFrom(r.Context()).ID != teacherID {
			writeError(w, http.StatusForbidden, "forbidden", "материалы добавляет teacher этого урока")
			return
		}

		var upload *uploadResponse
		if file != nil {
			if a.storage == nil {
				writeError(w, http.StatusServiceUnavailable, "storage_not_configured", "хранилище не настроено")
				return
			}
			// Ссылка подписывается до вставки строки: не вышло — в БД
			// не остаётся материала, ждущего загрузки, которой не будет
			if upload, err = a.presignUpload(r.Context(), lessonID, file); err != nil {
				internalError(w, "presign upload", err)
				return
			}
		}
		m, err := a.store.CreateMaterial(r.Context(), lessonID, kind, title, req.BodyMD, file)
		if err != nil {
			internalError(w, "create material", err)
			return
		}
		writeJSON(w, http.StatusCreated, createMaterialResponse{materialResponse: toMaterialResponse(*m), Upload: upload})
	}
}

// validFile проверяет заявку на файл и возвращает его метаданные с ключом
// ещё не выбранным; nil и текст ошибки — если заявка не проходит.
func validFile(req fileRequest) (*store.MaterialFile, string) {
	name, ok := cleanFileName(req.Name)
	if !ok {
		return nil, "file.name — имя файла до " + strconv.Itoa(maxFileNameRunes) + " символов"
	}
	contentType, _, err := mime.ParseMediaType(req.ContentType)
	if _, allowed := materialFileTypes[contentType]; err != nil || !allowed {
		return nil, "тип файла не поддерживается: PDF, DOCX, PPTX, TXT, PNG, JPEG, WebP, MP3, M4A, OGG, MP4"
	}
	if req.Size <= 0 || req.Size > maxMaterialFileBytes {
		return nil, "размер файла — от 1 байта до " + strconv.Itoa(maxMaterialFileBytes>>20) + " МБ"
	}
	return &store.MaterialFile{Name: name, ContentType: contentType, Size: req.Size}, ""
}

// cleanFileName — имя файла от браузера без пути: в ключ оно не попадает,
// но показывается людям и уходит в Content-Disposition.
func cleanFileName(raw string) (string, bool) {
	name := strings.TrimSpace(raw)
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = strings.TrimSpace(name[i+1:])
	}
	if name == "" || name == "." || name == ".." || !utf8.ValidString(name) ||
		utf8.RuneCountInString(name) > maxFileNameRunes {
		return "", false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", false
		}
	}
	return name, true
}

// presignUpload выбирает ключ под materials/{lesson_id}/ и подписывает
// загрузку ровно заявленного файла: тип, длина и имя для скачивания входят
// в подпись. Ключ записывается в file.
func (a *API) presignUpload(ctx context.Context, lessonID int64, file *store.MaterialFile) (*uploadResponse, error) {
	key, err := storage.NewMaterialKey(lessonID, materialFileTypes[file.ContentType])
	if err != nil {
		return nil, err
	}
	// attachment: ключ случайный, и только так браузер сохранит файл
	// под человеческим именем; не-ASCII имя кодируется по RFC 2231
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": file.Name})
	if disposition == "" {
		return nil, errors.New("content-disposition для имени файла не собрался")
	}
	put, err := a.storage.PresignPut(ctx, key, file.ContentType, disposition, file.Size, uploadURLTTL)
	if err != nil {
		return nil, err
	}
	file.S3Key = key
	headers := make(map[string]string, len(put.Header))
	for name := range put.Header {
		headers[name] = put.Header.Get(name)
	}
	return &uploadResponse{
		URL: put.URL, Method: http.MethodPut, Headers: headers,
		ExpiresAt: time.Now().Add(uploadURLTTL),
	}, nil
}

// materialForTeacher — материал из пути и проверка, что запрос делает
// преподаватель его урока; иначе ответ уже записан и ok = false.
func (a *API) materialForTeacher(w http.ResponseWriter, r *http.Request) (*store.Material, bool) {
	id, ok := pathID(w, r)
	if !ok {
		return nil, false
	}
	m, teacherID, err := a.store.MaterialWithTeacher(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "материал не найден")
		return nil, false
	}
	if err != nil {
		internalError(w, "load material", err)
		return nil, false
	}
	if userFrom(r.Context()).ID != teacherID {
		writeError(w, http.StatusForbidden, "forbidden", "файлами распоряжается teacher этого урока")
		return nil, false
	}
	return m, true
}

// handleConfirmMaterialFile — браузер сообщил, что загрузил файл. api
// проверяет объект сам: его нет — 409, загрузка не та (размер, тип) —
// объект и материал удаляются, 422. Повтор после успеха — снова 200.
func (a *API) handleConfirmMaterialFile(w http.ResponseWriter, r *http.Request) {
	if a.storage == nil {
		writeError(w, http.StatusServiceUnavailable, "storage_not_configured", "хранилище не настроено")
		return
	}
	m, ok := a.materialForTeacher(w, r)
	if !ok {
		return
	}
	if m.File == nil {
		writeError(w, http.StatusConflict, "no_file", "у материала нет файла")
		return
	}
	if m.File.Ready() {
		writeJSON(w, http.StatusOK, toMaterialResponse(*m))
		return
	}
	info, err := a.storage.Stat(r.Context(), m.File.S3Key)
	if errors.Is(err, storage.ErrObjectNotFound) {
		writeError(w, http.StatusConflict, "upload_missing", "файл ещё не загружен в хранилище")
		return
	}
	if err != nil {
		internalError(w, "stat material file", err)
		return
	}
	// Тип сверяется без параметров: charset к text/plain хранилище вправе
	// дописать, подменить сам тип — нет
	storedType, _, _ := mime.ParseMediaType(info.ContentType)
	if info.Size != m.File.Size || info.Size > maxMaterialFileBytes || storedType != m.File.ContentType {
		// Не тот объект, что заявлен: лежать ему незачем, а материал без
		// годного файла — пустая строка в списке. Заливать заново.
		if err := a.removeMaterial(r.Context(), m); err != nil {
			internalError(w, "drop rejected upload", err)
			return
		}
		writeError(w, http.StatusUnprocessableEntity, "upload_rejected",
			"загруженный файл не совпал с заявленным — загрузите его заново")
		return
	}
	confirmed, err := a.store.ConfirmMaterialFile(r.Context(), m.ID)
	if errors.Is(err, store.ErrNotFound) {
		// Материал удалили, пока мы сверяли объект
		writeError(w, http.StatusNotFound, "not_found", "материал не найден")
		return
	}
	if err != nil {
		internalError(w, "confirm material file", err)
		return
	}
	writeJSON(w, http.StatusOK, toMaterialResponse(*confirmed))
}

// handleMaterialFileURL — presigned GET на файл материала. Граница та же,
// что у страницы урока: преподаватель урока и его участники.
func (a *API) handleMaterialFileURL(w http.ResponseWriter, r *http.Request) {
	if a.storage == nil {
		writeError(w, http.StatusServiceUnavailable, "storage_not_configured", "хранилище не настроено")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	m, teacherID, err := a.store.MaterialWithTeacher(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "материал не найден")
		return
	}
	if err != nil {
		internalError(w, "load material", err)
		return
	}
	u := userFrom(r.Context())
	allowed := u.ID == teacherID
	if !allowed {
		if allowed, err = a.store.IsLessonParticipant(r.Context(), m.LessonID, u.ID); err != nil {
			internalError(w, "check participant", err)
			return
		}
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "forbidden", "нет доступа к этому уроку")
		return
	}
	if m.File == nil {
		writeError(w, http.StatusNotFound, "no_file", "у материала нет файла")
		return
	}
	if !m.File.Ready() {
		writeError(w, http.StatusConflict, "file_not_ready", "файл ещё загружается")
		return
	}
	url, err := a.storage.PresignGet(r.Context(), m.File.S3Key, storage.PresignTTL)
	if err != nil {
		internalError(w, "presign", err)
		return
	}
	writeJSON(w, http.StatusOK, mediaURLResponse{URL: url, ExpiresAt: time.Now().Add(storage.PresignTTL)})
}

// handleDeleteMaterial — удалить материал вместе с его файлом.
func (a *API) handleDeleteMaterial(w http.ResponseWriter, r *http.Request) {
	m, ok := a.materialForTeacher(w, r)
	if !ok {
		return
	}
	if m.File != nil && a.storage == nil {
		writeError(w, http.StatusServiceUnavailable, "storage_not_configured", "хранилище не настроено")
		return
	}
	switch err := a.removeMaterial(r.Context(), m); {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "материал не найден")
	case err != nil:
		internalError(w, "delete material", err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// removeMaterial — сначала объект, потом строка: сорвалось удаление
// строки — повтор снова удалит (уже отсутствующий) объект и дойдёт до неё;
// в обратном порядке сбой оставил бы в бакете файл, на который никто
// не ссылается.
func (a *API) removeMaterial(ctx context.Context, m *store.Material) error {
	if m.File != nil {
		if err := a.storage.Delete(ctx, m.File.S3Key); err != nil {
			return err
		}
	}
	return a.store.DeleteMaterial(ctx, m.ID)
}

// deleteFiles — уборка объектов после отмены урока (каскад удалил строки
// материалов). Сбой не отменяет отмену: объект остаётся сиротой под
// materials/{lesson_id}/, и об этом остаётся строка в журнале.
func (a *API) deleteFiles(ctx context.Context, keys []string) {
	for _, key := range keys {
		if a.storage == nil {
			logf("файл %s отменённого урока не удалён: хранилище не настроено", key)
			continue
		}
		if err := a.storage.Delete(ctx, key); err != nil {
			logf("файл %s отменённого урока не удалён: %v", key, err)
		}
	}
}
