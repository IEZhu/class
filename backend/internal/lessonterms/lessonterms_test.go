package lessonterms

import (
	"os"
	"reflect"
	"testing"
)

// Сцена-образец (testdata/scene.json) — элементы Excalidraw 0.18 как их
// хранит whiteboards.scene: фигура с подписью внутри, свободный текст
// с переносом по ширине, удалённый текст, рисунок от руки, текст старого
// формата без originalText.
func TestSceneTexts(t *testing.T) {
	scene, err := os.ReadFile("testdata/scene.json")
	if err != nil {
		t.Fatal(err)
	}
	got, err := SceneTexts(scene)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"Running fast",
		"Homework: extraordinarily difficult",
		"The cats were running to school",
		"Blorpy 2026 cats!",
		"beautiful houses",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("тексты сцены %q\nхотели %q", got, want)
	}
	for _, bad := range []string{`{`, `{"elements":[]}`, `[1]`} {
		if _, err := SceneTexts([]byte(bad)); err == nil {
			t.Errorf("сцена %s разобрана без ошибки", bad)
		}
	}
	if got, err := SceneTexts([]byte(`[]`)); err != nil || len(got) != 0 {
		t.Fatalf("пустая доска: %q, %v", got, err)
	}
}

// Формы сводятся к лемме и считаются вместе, служебные слова и числа
// пропускаются; незнакомые (Blorpy) отсекает уже Known — по базе.
func TestTextWords(t *testing.T) {
	got := TextWords([]string{
		"Running fast", "Homework: extraordinarily difficult",
		"The cats were running to school", "Blorpy 2026 cats!", "beautiful houses",
	})
	want := []Word{
		{Lemma: "cat", Surface: "cats", Count: 2},
		{Lemma: "run", Surface: "Running", Count: 2},
		{Lemma: "beautiful", Surface: "beautiful", Count: 1},
		{Lemma: "blorpy", Surface: "Blorpy", Count: 1},
		{Lemma: "difficult", Surface: "difficult", Count: 1},
		{Lemma: "extraordinarily", Surface: "extraordinarily", Count: 1},
		{Lemma: "fast", Surface: "fast", Count: 1},
		{Lemma: "homework", Surface: "Homework", Count: 1},
		{Lemma: "house", Surface: "houses", Count: 1},
		{Lemma: "school", Surface: "school", Count: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("слова %+v\nхотели %+v", got, want)
	}
}
