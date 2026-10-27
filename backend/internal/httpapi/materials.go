package httpapi

import (
	"context"
	"errors"
	"mime"
	"net/http"
	"path"
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

// extensionAliases — другие расширения тех же типов: «фото.jpeg» остаётся
// как есть, а не превращается в «фото.jpg».
var extensionAliases = map[string]string{"jpeg": "jpg"}

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
	name, msg := cleanFileName(req.Name)
	if msg != "" {
		return nil, msg
	}
	contentType, _, err := mime.ParseMediaType(req.ContentType)
	ext, allowed := materialFileTypes[contentType]
	if err != nil || !allowed {
		return nil, "тип файла не поддерживается: PDF, DOCX, PPTX, TXT, PNG, JPEG, WebP, MP3, M4A, OGG, MP4"
	}
	if req.Size <= 0 || req.Size > maxMaterialFileBytes {
		return nil, "размер файла — от 1 байта до " + strconv.Itoa(maxMaterialFileBytes>>20) + " МБ"
	}
	name = downloadName(name, ext)
	if utf8.RuneCountInString(name) > maxFileNameRunes {
		return nil, "file.name — имя файла до " + strconv.Itoa(maxFileNameRunes) + " символов"
	}
	return &store.MaterialFile{Name: name, ContentType: contentType, Size: req.Size}, ""
}

// cleanFileName — имя файла от браузера без пути: в ключ оно не попадает,
// но показывается людям и уходит в Content-Disposition. Пустая строка
// вторым значением — имя годится, иначе это текст ошибки.
func cleanFileName(raw string) (string, string) {
	name := strings.TrimSpace(raw)
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = strings.TrimSpace(name[i+1:])
	}
	if name == "" || name == "." || name == ".." {
		return "", "file.name — нужно имя файла"
	}
	if !utf8.ValidString(name) {
		return "", "file.name — имя файла не в UTF-8"
	}
	for _, r := range name {
		// Cf — невидимые символы форматирования. Среди них U+202E: на
		// экране «homework\u202efdp.exe» читается как «homeworkexe.pdf»
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return "", "file.name — в имени файла управляющие или невидимые символы"
		}
	}
	return name, ""
}

// downloadName — имя, под которым файл сохранят участники: расширение
// задаёт заявленный тип, а не имя от клиента. Иначе заявка
// «application/pdf» с именем setup.exe сохранилась бы у студента как
// программа. Совпадающее расширение (и jpeg у JPEG) остаётся как есть,
// чужое заменяется, отсутствующее дописывается.
func downloadName(name, ext string) string {
	cur := path.Ext(name)
	if e := strings.ToLower(strings.TrimPrefix(cur, ".")); e == ext || extensionAliases[e] == ext {
		return name
	}
	return strings.TrimSuffix(name, cur) + "." + ext
}

// presignUpload выбирает постоянный ключ под materials/{lesson_id}/
// и подписывает загрузку ровно заявленного файла на его копию под
// materials-pending/: тип, длина и имя для скачивания входят в подпись.
// На постоянный ключ файл переносит только confirm, поэтому ссылка на
// загрузку подтверждённый файл не перезапишет. Ключ записывается в file.
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
	put, err := a.storage.PresignPut(ctx, storage.PendingMaterialKey(key), file.ContentType, disposition, file.Size, uploadURLTTL)
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
// объект и материал удаляются, 422. Годный объект копируется внутри
// бакета на постоянный ключ, ETag копии запоминается. Повтор после
// успеха — снова 200.
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
	pending := storage.PendingMaterialKey(m.File.S3Key)
	info, err := a.storage.Stat(r.Context(), pending)
	if errors.Is(err, storage.ErrObjectNotFound) {
		writeError(w, http.StatusConflict, "upload_missing", "файл ещё не загружен в хранилище")
		return
	}
	if err != nil {
		internalError(w, "stat material upload", err)
		return
	}
	if !matchesDeclared(info, m.File) {
		a.rejectUpload(w, r, m)
		return
	}
	// Повтор после сбоя на любом шаге ниже безопасен: загрузка лежит,
	// пока строка не подтверждена, и копируется заново
	if err := a.storage.Copy(r.Context(), pending, m.File.S3Key); err != nil {
		internalError(w, "promote material file", err)
		return
	}
	final, err := a.storage.Stat(r.Context(), m.File.S3Key)
	if err != nil {
		internalError(w, "stat material file", err)
		return
	}
	if !matchesDeclared(final, m.File) {
		a.rejectUpload(w, r, m)
		return
	}
	if final.ETag == "" {
		internalError(w, "stat material file", errors.New("хранилище не вернуло ETag "+m.File.S3Key))
		return
	}
	confirmed, err := a.store.ConfirmMaterialFile(r.Context(), m.ID, final.ETag)
	if errors.Is(err, store.ErrNotFound) {
		// Материал удалили, пока мы сверяли объект: копия на постоянном
		// ключе могла появиться уже после его уборки — её уберёт worker
		if err := a.store.QueueMaterialFilesDelete(r.Context(), []string{m.File.S3Key}); err != nil {
			logf("копия файла удалённого материала %s осталась в хранилище: %v", m.File.S3Key, err)
		}
		writeError(w, http.StatusNotFound, "not_found", "материал не найден")
		return
	}
	if err != nil {
		internalError(w, "confirm material file", err)
		return
	}
	// Загрузку не удалось убрать — её сотрёт lifecycle бакета через сутки
	if err := a.storage.Delete(r.Context(), pending); err != nil {
		logf("загрузка %s осталась до lifecycle: %v", pending, err)
	}
	writeJSON(w, http.StatusOK, toMaterialResponse(*confirmed))
}

// matchesDeclared — объект тот, что заявлен: размер и тип. Тип сверяется
// без параметров: charset к text/plain хранилище вправе дописать,
// подменить сам тип — нет.
func matchesDeclared(info storage.ObjectInfo, f *store.MaterialFile) bool {
	storedType, _, _ := mime.ParseMediaType(info.ContentType)
	return info.Size == f.Size && info.Size <= maxMaterialFileBytes && storedType == f.ContentType
}

// rejectUpload — загружен не тот объект, что заявлен: лежать ему незачем,
// а материал без годного файла — пустая строка в списке. Заливать заново.
func (a *API) rejectUpload(w http.ResponseWriter, r *http.Request, m *store.Material) {
	switch err := a.removeMaterial(r.Context(), m); {
	case errors.Is(err, store.ErrNotFound):
		// Материал удалили параллельно — отвергать уже нечего
		writeError(w, http.StatusNotFound, "not_found", "материал не найден")
	case err != nil:
		internalError(w, "drop rejected upload", err)
	default:
		writeError(w, http.StatusUnprocessableEntity, "upload_rejected",
			"загруженный файл не совпал с заявленным — загрузите его заново")
	}
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
	// Ключом хранилища владеет и LiveKit Egress (ADR-014): объект, который
	// перезаписали в обход api после подтверждения, участникам не отдаётся
	info, err := a.storage.Stat(r.Context(), m.File.S3Key)
	switch {
	case errors.Is(err, storage.ErrObjectNotFound):
		writeError(w, http.StatusConflict, "file_missing", "файла нет в хранилище — его нужно загрузить заново")
		return
	case err != nil:
		internalError(w, "stat material file", err)
		return
	case info.ETag != m.File.ETag || !matchesDeclared(info, m.File):
		logf("файл материала %d изменён в хранилище в обход api: %s", m.ID, m.File.S3Key)
		writeError(w, http.StatusConflict, "file_changed", "файл в хранилище изменён после загрузки — его нужно загрузить заново")
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

// removeMaterial — сначала объекты файла, потом строка: сорвалось
// удаление строки — повтор снова удалит (уже отсутствующие) объекты
// и дойдёт до неё. Хранилище не ответило — строка всё равно удаляется,
// а ключ той же транзакцией уходит в джобу material_files_delete: её
// worker повторяет, пока объекты не исчезнут (ADR-024).
func (a *API) removeMaterial(ctx context.Context, m *store.Material) error {
	var left []string
	if m.File != nil {
		for _, key := range storage.MaterialFileKeys(m.File.S3Key) {
			if err := a.storage.Delete(ctx, key); err != nil {
				logf("файл материала %d удалит worker: %v", m.ID, err)
				left = []string{m.File.S3Key}
				break
			}
		}
	}
	return a.store.DeleteMaterial(ctx, m.ID, left)
}
