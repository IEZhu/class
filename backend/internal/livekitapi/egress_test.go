package livekitapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	"google.golang.org/protobuf/encoding/protojson"
)

// Серверный API LiveKit живёт по тому же адресу, что и комната, но по
// http(s): схема меняется только в начале URL.
func TestNewBaseURL(t *testing.T) {
	cases := []struct{ url, want string }{
		{"wss://class.livekit.cloud", "https://class.livekit.cloud"},
		{"wss://class.livekit.cloud/", "https://class.livekit.cloud"},
		{"ws://localhost:7880", "http://localhost:7880"},
		{"https://class.livekit.cloud", "https://class.livekit.cloud"},
		// «ws://» не в начале — не схема, его не трогаем
		{"https://proxy.example/ws://x", "https://proxy.example/ws://x"},
	}
	for _, c := range cases {
		if got := New(c.url, "k", "s").baseURL; got != c.want {
			t.Errorf("New(%q).baseURL = %q, хотели %q", c.url, got, c.want)
		}
	}
}

// StopEgress — один POST Twirp с грантом RoomRecord. Запись уже кончилась
// или её нет (failed_precondition, not_found) — не ошибка, иначе — ошибка
// с причиной из тела.
func TestStopEgress(t *testing.T) {
	for _, c := range []struct {
		status           int
		body             string
		stopped, wantErr bool
	}{
		{http.StatusOK, `{"egressId":"EG_1","status":"EGRESS_ENDING"}`, true, false},
		{http.StatusPreconditionFailed, `{"code":"failed_precondition","msg":"egress with status EGRESS_COMPLETE cannot be stopped"}`, false, false},
		{http.StatusNotFound, `{"code":"not_found","msg":"egress does not exist"}`, false, false},
		{http.StatusUnauthorized, `{"code":"unauthenticated","msg":"permissions denied"}`, false, true},
		{http.StatusInternalServerError, `{"code":"internal","msg":"boom"}`, false, true},
	} {
		var got livekit.StopEgressRequest
		var grants *auth.ClaimGrants
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/twirp/livekit.Egress/StopEgress" {
				http.NotFound(w, r)
				return
			}
			body, _ := io.ReadAll(r.Body)
			if err := protojson.Unmarshal(body, &got); err != nil {
				t.Errorf("тело запроса: %v", err)
			}
			v, err := auth.ParseAPIToken(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			if err == nil {
				_, grants, err = v.Verify("secret")
			}
			if err != nil {
				t.Errorf("токен: %v", err)
			}
			w.WriteHeader(c.status)
			_, _ = w.Write([]byte(c.body))
		}))
		stopped, err := New(srv.URL, "key", "secret").StopEgress(context.Background(), "EG_1")
		srv.Close()
		if stopped != c.stopped || (err != nil) != c.wantErr {
			t.Errorf("HTTP %d: (%v, %v), хотели остановку %v и ошибку %v", c.status, stopped, err, c.stopped, c.wantErr)
		}
		if c.wantErr && err != nil && !strings.Contains(err.Error(), "EG_1") {
			t.Errorf("HTTP %d: в ошибке нет id Egress: %v", c.status, err)
		}
		if got.EgressId != "EG_1" || grants == nil || grants.Video == nil || !grants.Video.RoomRecord {
			t.Errorf("HTTP %d: запрос %v, гранты %+v", c.status, got.EgressId, grants)
		}
	}
}
