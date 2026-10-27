package srs

import (
	"testing"
	"time"
)

// t0 — подставленные «часы» тестов: график зависит только от переданного
// момента, а не от времени запуска.
var t0 = time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)

func review(t *testing.T, s *Scheduler, c Card, r Rating, at time.Time) Card {
	t.Helper()
	next, err := s.Review(c, r, at)
	if err != nil {
		t.Fatal(err)
	}
	return next
}

// graduated — карточка, выведенная в review оценкой easy в момент at.
func graduated(t *testing.T, s *Scheduler, at time.Time) Card {
	t.Helper()
	c := review(t, s, NewCard(at), Easy, at)
	if c.State != StateReview {
		t.Fatalf("easy у новой карточки: %+v", c)
	}
	return c
}

func TestNewCardRatings(t *testing.T) {
	s := NewScheduler()
	c := NewCard(t0)
	if c.State != StateNew || !c.Due.Equal(t0) || c.LastReview != nil || Status(c) != 1 {
		t.Fatalf("новая карточка: %+v", c)
	}
	// Шаги обучения go-fsrs по умолчанию — 1 и 10 минут: again — первый
	// шаг, hard — среднее двух, good — второй, easy — сразу в review
	steps := map[Rating]time.Duration{Again: time.Minute, Hard: 6 * time.Minute, Good: 10 * time.Minute}
	for r, delay := range steps {
		next := review(t, s, c, r, t0)
		if next.State != StateLearning || !next.Due.Equal(t0.Add(delay)) || next.ScheduledDays != 0 {
			t.Fatalf("%s: %+v, ждали learning через %v", r, next, delay)
		}
		if next.Reps != 1 || next.Lapses != 0 || next.LastReview == nil || !next.LastReview.Equal(t0) {
			t.Fatalf("%s: счётчики %+v", r, next)
		}
		if Status(next) != 2 {
			t.Fatalf("%s: статус %d", r, Status(next))
		}
	}
	easy := review(t, s, c, Easy, t0)
	if easy.State != StateReview || easy.ScheduledDays < 1 ||
		!easy.Due.Equal(t0.Add(time.Duration(easy.ScheduledDays)*24*time.Hour)) {
		t.Fatalf("easy: %+v", easy)
	}
	// Исходная карточка не меняется: расчёт возвращает новую
	if c.State != StateNew || c.Reps != 0 {
		t.Fatalf("Review изменил аргумент: %+v", c)
	}
}

func TestLearningStepsGraduate(t *testing.T) {
	s := NewScheduler()
	c := review(t, s, NewCard(t0), Good, t0)
	at := c.Due
	c = review(t, s, c, Good, at)
	if c.State != StateReview || c.ScheduledDays < 1 || c.Reps != 2 || c.RemainingSteps != 0 {
		t.Fatalf("второй good: %+v", c)
	}
	if !c.Due.Equal(at.Add(time.Duration(c.ScheduledDays) * 24 * time.Hour)) {
		t.Fatalf("due не по интервалу: %+v", c)
	}
	// again на шаге обучения возвращает к первому шагу
	again := review(t, s, review(t, s, NewCard(t0), Good, t0), Again, at)
	if again.State != StateLearning || !again.Due.Equal(at.Add(time.Minute)) {
		t.Fatalf("again в обучении: %+v", again)
	}
}

func TestReviewRatingsOrderIntervals(t *testing.T) {
	s := NewScheduler()
	c := graduated(t, s, t0)
	at := c.Due
	got := map[Rating]Card{}
	for _, r := range []Rating{Again, Hard, Good, Easy} {
		got[r] = review(t, s, c, r, at)
	}
	if !got[Again].Due.Before(got[Hard].Due) || !got[Hard].Due.Before(got[Good].Due) ||
		!got[Good].Due.Before(got[Easy].Due) {
		t.Fatalf("интервалы не по возрастанию оценок: %+v", got)
	}
	a := got[Again]
	if a.State != StateRelearning || a.Lapses != 1 || !a.Due.Equal(at.Add(10*time.Minute)) || Status(a) != 2 {
		t.Fatalf("again в review — переобучение через 10 минут: %+v", a)
	}
	for _, r := range []Rating{Hard, Good, Easy} {
		if next := got[r]; next.State != StateReview || next.Lapses != 0 || next.Reps != c.Reps+1 {
			t.Fatalf("%s: %+v", r, next)
		}
	}
}

func TestGoodOnTimeGrowsInterval(t *testing.T) {
	s := NewScheduler()
	c := graduated(t, s, t0)
	prev := c.ScheduledDays
	for range 5 {
		c = review(t, s, c, Good, c.Due)
		if c.ScheduledDays <= prev {
			t.Fatalf("интервал не вырос: %d после %d (%+v)", c.ScheduledDays, prev, c)
		}
		prev = c.ScheduledDays
	}
	if Status(c) != 5 {
		t.Fatalf("после пяти good вовремя слово не «знаю»: %d (%+v)", Status(c), c)
	}
}

func TestReviewIsDeterministic(t *testing.T) {
	// Без fuzz одна и та же оценка в тот же момент даёт ту же дату
	a, b := NewScheduler(), NewScheduler()
	ca, cb := graduated(t, a, t0), graduated(t, b, t0)
	for range 3 {
		ca, cb = review(t, a, ca, Good, ca.Due), review(t, b, cb, Good, cb.Due)
	}
	if !ca.Due.Equal(cb.Due) || ca.Stability != cb.Stability {
		t.Fatalf("разные результаты: %+v и %+v", ca, cb)
	}
}

func TestStatusThresholds(t *testing.T) {
	cases := []struct {
		card Card
		want int
	}{
		{Card{State: StateNew}, 1},
		{Card{State: StateLearning}, 2},
		{Card{State: StateRelearning, ScheduledDays: 40}, 2},
		{Card{State: StateReview, ScheduledDays: 1}, 3},
		{Card{State: StateReview, ScheduledDays: 6}, 3},
		{Card{State: StateReview, ScheduledDays: 7}, 4},
		{Card{State: StateReview, ScheduledDays: 29}, 4},
		{Card{State: StateReview, ScheduledDays: 30}, 5},
	}
	for _, tc := range cases {
		if got := Status(tc.card); got != tc.want {
			t.Errorf("Status(%+v) = %d, want %d", tc.card, got, tc.want)
		}
	}
}

func TestReviewRejectsInvalidInput(t *testing.T) {
	s := NewScheduler()
	if _, err := s.Review(NewCard(t0), Rating("perfect"), t0); err == nil {
		t.Fatal("неизвестная оценка принята")
	}
	if _, err := s.Review(Card{Due: t0, State: "suspended"}, Good, t0); err == nil {
		t.Fatal("неизвестное состояние принято")
	}
	// Повторение раньше прошлого — часы ушли назад: go-fsrs отказывает
	c := graduated(t, s, t0)
	if _, err := s.Review(c, Good, t0.Add(-time.Hour)); err == nil {
		t.Fatal("повторение раньше прошлого принято")
	}
}

func TestParseRating(t *testing.T) {
	for _, r := range []string{"again", "hard", "good", "easy"} {
		if got, ok := ParseRating(r); !ok || string(got) != r {
			t.Fatalf("ParseRating(%q) = %q, %v", r, got, ok)
		}
	}
	for _, r := range []string{"", "Good", "manual", "0"} {
		if _, ok := ParseRating(r); ok {
			t.Fatalf("ParseRating(%q) принял", r)
		}
	}
}
