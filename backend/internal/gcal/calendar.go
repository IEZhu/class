package gcal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/oauth2"
)

// Event — событие урока в основном календаре преподавателя.
type Event struct {
	// ID назначаем сами (lessons.gcal_event_id, ADR-023): повторный insert
	// с тем же id получает 409, а не второе событие.
	ID          string
	Summary     string
	Description string
	Start, End  time.Time
	Attendees   []string
}

// Calendar — вызовы Calendar API от имени владельца refresh token.
// Access token получается при первом вызове и переиспользуется в
// следующих: insert, получивший 409, и update после него — один обмен.
type Calendar struct {
	http *http.Client
	base string
}

// Calendar открывает основной календарь владельца refresh token. ctx
// ограничивает и обновление токена, поэтому клиент живёт одну джобу.
func (c *Client) Calendar(ctx context.Context, refreshToken string) *Calendar {
	ctx = c.withHTTP(ctx)
	src := c.oauth.TokenSource(ctx, &oauth2.Token{RefreshToken: refreshToken})
	return &Calendar{http: oauth2.NewClient(ctx, src), base: c.calendarURL + "/calendars/primary/events"}
}

// Insert — events.insert; ErrConflict — событие с этим id уже есть.
func (c *Calendar) Insert(ctx context.Context, ev Event) error {
	return c.do(ctx, http.MethodPost, "", eventBodyOf(ev))
}

// Update — events.update: событие перезаписывается целиком (у метода нет
// patch-семантики), то есть правки руками в Google при переносе теряются.
func (c *Calendar) Update(ctx context.Context, ev Event) error {
	return c.do(ctx, http.MethodPut, "/"+url.PathEscape(ev.ID), eventBodyOf(ev))
}

// Delete — events.delete; ErrNotFound — события нет или оно уже удалено.
func (c *Calendar) Delete(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/"+url.PathEscape(id), nil)
}

// do — один вызов events с sendUpdates=all: участники получают
// приглашение, перенос и отмену письмом от Google.
func (c *Calendar) do(ctx context.Context, method, path string, body any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path+"?sendUpdates=all", reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		// Обновление access token упало на invalid_grant: refresh token
		// отозван или истёк, повторять бесполезно
		var re *oauth2.RetrieveError
		if errors.As(err, &re) && re.ErrorCode == "invalid_grant" {
			return ErrRevoked
		}
		return fmt.Errorf("gcal: %s events%s: %w", method, path, err)
	}
	defer res.Body.Close() //nolint:errcheck // тело только читаем
	switch {
	case res.StatusCode >= 200 && res.StatusCode < 300:
		return nil
	case res.StatusCode == http.StatusNotFound, res.StatusCode == http.StatusGone:
		return ErrNotFound
	case res.StatusCode == http.StatusConflict:
		return ErrConflict
	}
	// 403/429 rateLimitExceeded, 5xx и прочее — ошибка джобы и повтор
	// с backoff очереди (ADR-013)
	return fmt.Errorf("gcal: %s events%s: %s", method, path, responseError(res))
}

type eventBody struct {
	ID          string          `json:"id"`
	Summary     string          `json:"summary"`
	Description string          `json:"description"`
	Start       eventTime       `json:"start"`
	End         eventTime       `json:"end"`
	Attendees   []eventAttendee `json:"attendees"`
	// confirmed явно: update перезаписывает событие целиком
	Status string `json:"status"`
}

type eventTime struct {
	DateTime string `json:"dateTime"`
}

type eventAttendee struct {
	Email string `json:"email"`
}

func eventBodyOf(ev Event) eventBody {
	attendees := make([]eventAttendee, 0, len(ev.Attendees))
	for _, email := range ev.Attendees {
		attendees = append(attendees, eventAttendee{Email: email})
	}
	return eventBody{
		ID:          ev.ID,
		Summary:     ev.Summary,
		Description: ev.Description,
		Start:       eventTime{DateTime: ev.Start.UTC().Format(time.RFC3339)},
		End:         eventTime{DateTime: ev.End.UTC().Format(time.RFC3339)},
		Attendees:   attendees,
		Status:      "confirmed",
	}
}
