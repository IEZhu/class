package tatoeba

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// Фикстура testdata/ — строки в формате поязыковых выгрузок Tatoeba
// (проверен на выгрузке 2026-10-24) с выдуманными предложениями.

func TestParseSentence(t *testing.T) {
	s, err := parseSentence([]byte("1277\teng\t I have to go to sleep. \tvinhan\t\\N\t2009-11-25 23:20:59"), "eng")
	if err != nil || s != (sentence{ID: 1277, Text: "I have to go to sleep.", Author: "vinhan"}) {
		t.Fatalf("строка выгрузки: %+v, %v", s, err)
	}
	// \N в имени — предложение без владельца
	s, err = parseSentence([]byte("5\trus\tОн побежал домой.\t\\N\t\\N\t\\N"), "rus")
	if err != nil || s.Author != "" || s.Text != "Он побежал домой." {
		t.Fatalf("без владельца: %+v, %v", s, err)
	}
	for _, line := range []string{
		"",
		"1\teng\tToo few fields.\tCK",
		"1\teng\tToo many fields.\tCK\t\\N\t\\N\textra",
		"x\teng\tNot a number.\tCK\t\\N\t\\N",
		"0\teng\tZero id.\tCK\t\\N\t\\N",
		"-3\teng\tNegative id.\tCK\t\\N\t\\N",
		"1\trus\tWrong language.\tCK\t\\N\t\\N",
		"1\teng\t   \tCK\t\\N\t\\N",
		"1\teng\t\xff\xfe\tCK\t\\N\t\\N",
		"1\teng\tNUL \x00 inside.\tCK\t\\N\t\\N",
		"1\teng\tBad author.\t\xff\t\\N\t\\N",
	} {
		if _, err := parseSentence([]byte(line), "eng"); !errors.Is(err, errMalformed) {
			t.Errorf("%q: %v, ждали испорченную строку", line, err)
		}
	}
}

func TestParseLink(t *testing.T) {
	if p, err := parseLink([]byte("1276\t5409")); err != nil || p != (pair{en: 1276, ru: 5409}) {
		t.Fatalf("пара: %+v, %v", p, err)
	}
	for _, line := range []string{"", "13", "x\t1", "1\tx", "1\t2\t3", "0\t1", "1\t-1", " 1\t2"} {
		if _, err := parseLink([]byte(line)); !errors.Is(err, errMalformed) {
			t.Errorf("%q: %v, ждали испорченную строку", line, err)
		}
	}
}

// Леммы — того же лемматизатора, что и клик: форма предложения находится
// по лемме термина.
func TestLemmas(t *testing.T) {
	for text, want := range map[string]string{
		"He ran home.":                  "he home run",
		"We are running late.":          "be late run we",
		"Run! Run! RUN!":                "run",
		"Tom's dogs aren't barking.":    "aren't bark dog tom",
		"It’s a well-known fact.":       "a fact it's know well well-known",
		"The students' books, 'please'": "book please student the",
		"In 1999 I was 3 years old.":    "be i in old year",
		"2 + 2 = 4":                     "",
		"--- !!!":                       "",
	} {
		if got := strings.Join(Lemmas(text), " "); got != want {
			t.Errorf("Lemmas(%q) = %q, ждали %q", text, got, want)
		}
	}
	if got := Lemmas("Да, я бегу."); !slices.Equal(got, []string{"бегу", "да", "я"}) {
		t.Errorf("не английский текст: %q", got)
	}
}
