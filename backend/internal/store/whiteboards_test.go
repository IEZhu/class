package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func el(id string, version, nonce int64, extra string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"id":%q,"version":%d,"versionNonce":%d,"type":"text"%s}`,
		id, version, nonce, extra))
}

func ids(elements []json.RawMessage) string {
	out := make([]string, 0, len(elements))
	for _, raw := range elements {
		var h struct {
			ID      string `json:"id"`
			Version int64  `json:"version"`
		}
		_ = json.Unmarshal(raw, &h)
		out = append(out, fmt.Sprintf("%s@%d", h.ID, h.Version))
	}
	return strings.Join(out, ",")
}

// Правило Excalidraw: бо́льшая version, при равных — меньший versionNonce.
// Сохранённое, но не присланное остаётся; удаление побеждает отставшего.
func TestMergeElements(t *testing.T) {
	stored := []json.RawMessage{
		el("a", 2, 50, ""),
		el("b", 3, 10, `,"isDeleted":true`),
		el("c", 1, 10, ""),
		el("d", 4, 10, ""),
	}
	incoming := []json.RawMessage{
		el("d", 4, 5, ""),                   // та же версия, меньший nonce — берём присланный
		el("a", 3, 99, `,"text":"new"`),     // новее — берём присланный
		el("b", 2, 1, `,"isDeleted":false`), // отставший не воскрешает удалённый
		el("e", 1, 7, ""),                   // новый элемент
		el("c", 1, 20, ""),                  // та же версия, больший nonce — остаётся сохранённый
	}
	merged, changed, err := mergeElements(stored, incoming)
	if err != nil || !changed {
		t.Fatalf("merge: changed=%v err=%v", changed, err)
	}
	if got := ids(merged); got != "d@4,a@3,b@3,e@1,c@1" {
		t.Fatalf("merged %s", got)
	}
	if !strings.Contains(string(merged[1]), `"text":"new"`) || !strings.Contains(string(merged[2]), `"isDeleted":true`) ||
		!strings.Contains(string(merged[4]), `"versionNonce":10`) || !strings.Contains(string(merged[0]), `"versionNonce":5`) {
		t.Fatalf("wrong winners: %s", merged)
	}

	// Сохранение отставшего участника, у которого есть только часть сцены,
	// ничего не теряет и ничего не меняет
	merged, changed, err = mergeElements(merged, []json.RawMessage{el("c", 1, 10, "")})
	if err != nil || changed || ids(merged) != "c@1,d@4,a@3,b@3,e@1" {
		t.Fatalf("stale partial save: %s changed=%v err=%v", ids(merged), changed, err)
	}
	if _, changed, _ := mergeElements(nil, nil); changed {
		t.Fatal("empty save changed an empty scene")
	}
}

// Когда index есть у всех, итог упорядочен по нему, как orderByFractionalIndex
// Excalidraw: элемент, нарисованный выше другого, остаётся выше и после
// слияния сохранений из разных вкладок.
func TestMergeElementsOrdersByFractionalIndex(t *testing.T) {
	stored := []json.RawMessage{el("top", 1, 1, `,"index":"a2"`), el("bottom", 1, 1, `,"index":"a0"`)}
	incoming := []json.RawMessage{el("mid", 1, 1, `,"index":"a1"`), el("tie", 1, 1, `,"index":"a1"`)}
	merged, _, err := mergeElements(stored, incoming)
	if err != nil || ids(merged) != "bottom@1,mid@1,tie@1,top@1" {
		t.Fatalf("ordered %s (%v)", ids(merged), err)
	}
	// Без индекса у кого-то порядок не трогаем
	merged, _, err = mergeElements(stored, []json.RawMessage{el("legacy", 1, 1, "")})
	if err != nil || ids(merged) != "legacy@1,top@1,bottom@1" {
		t.Fatalf("unindexed %s (%v)", ids(merged), err)
	}
}

func TestMergeElementsRejectsGarbage(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `"x"`, `{}`, `{"id":"a","version":1}`,
		`{"id":"a","version":1.5,"versionNonce":1}`, `{"id":"","version":1,"versionNonce":1}`,
		`{"id":"` + strings.Repeat("x", maxElementIDLen+1) + `","version":1,"versionNonce":1}`} {
		if _, _, err := mergeElements(nil, []json.RawMessage{json.RawMessage(raw)}); !errors.Is(err, ErrBadScene) {
			t.Errorf("%s: err = %v, want ErrBadScene", raw, err)
		}
	}
	if _, _, err := mergeElements(nil, []json.RawMessage{el("a", 1, 1, ""), el("a", 2, 1, "")}); !errors.Is(err, ErrBadScene) {
		t.Errorf("duplicate id: err = %v", err)
	}
}

func TestWhiteboardFile(t *testing.T) {
	body, err := WhiteboardFile(json.RawMessage(`[{"id":"a","type":"text","text":"apple"}]`))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"type":"excalidraw","version":2,"source":"lingua-class","elements":[{"id":"a","type":"text","text":"apple"}],"appState":{},"files":{}}`
	if string(body) != want {
		t.Fatalf("file %s", body)
	}
}
