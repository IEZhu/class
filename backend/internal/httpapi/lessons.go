package httpapi

import "net/http"

// handleCreateLessonStub — S0-4 (CRUD ядра) заменит на реализацию.
// Маршрут уже обёрнут requireRole("teacher"): студент получает 403 (DoD S0-3),
// преподаватель — честный 501 до S0-4.
func (a *API) handleCreateLessonStub(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotImplemented, "not_implemented", "создание урока появится в S0-4")
}
