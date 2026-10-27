// Package storagetest — fake S3 для тестов: принимает PutObject
// path-style (как R2) и хранит объекты в памяти. Настоящий бакет
// тестам не нужен и недоступен.
package storagetest

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/IEZhu/class/backend/internal/storage"
)

const Bucket = "lingua-class"

// Object — что пришло в PutObject.
type Object struct {
	ContentType string
	Body        []byte
}

type Server struct {
	srv     *httptest.Server
	mu      sync.Mutex
	objects map[string]Object
	fail    bool
}

// New поднимает fake на время теста.
func New(t *testing.T) *Server {
	t.Helper()
	s := &Server{objects: map[string]Object{}}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	return s
}

// Config — конфиг storage.New, нацеленный на fake.
func (s *Server) Config() storage.Config {
	return storage.Config{Bucket: Bucket, Endpoint: s.srv.URL, AccessKey: "fixture-key", SecretKey: "fixture-secret"}
}

// Fail — пока true, каждый PutObject получает 403, как с истёкшим
// токеном R2 (D-6). 403 SDK не повторяет, тест не ждёт его ретраев.
func (s *Server) Fail(fail bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail = fail
}

// Object — объект по ключу; false — его не клали.
func (s *Server) Object(key string) (Object, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.objects[key]
	return o, ok
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	key, ok := strings.CutPrefix(r.URL.Path, "/"+Bucket+"/")
	if r.Method != http.MethodPut || !ok || key == "" {
		http.Error(w, "fake s3: only path-style PutObject", http.StatusNotImplemented)
		return
	}
	// Подпись и учётные данные fake не проверяет — это делает SDK
	// на нашей стороне и хранилище на своей
	if r.Header.Get("Authorization") == "" {
		http.Error(w, "fake s3: unsigned request", http.StatusForbidden)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		http.Error(w, "fake s3: failing on purpose", http.StatusForbidden)
		return
	}
	s.objects[key] = Object{ContentType: r.Header.Get("Content-Type"), Body: body}
	w.Header().Set("ETag", `"fixture"`)
	w.WriteHeader(http.StatusOK)
}
