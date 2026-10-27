// Package srs — график повторений FSRS (S4-1…S4-3, ADR-036) поверх
// go-fsrs: параметры планировщика стенда, оценки и состояния карточки
// в терминах схемы (srs_cards, srs_reviews) и правило, по которому
// повторение двигает статус слова в личном словаре. Пакет без SQL и
// без часов: время передаёт вызывающий, поэтому график проверяется
// тестами с подставленным моментом.
package srs

import (
	"fmt"
	"time"

	fsrs "github.com/open-spaced-repetition/go-fsrs/v4"
)

// Rating — оценка повторения, как её принимает api и пишет
// srs_reviews.rating.
type Rating string

const (
	Again Rating = "again"
	Hard  Rating = "hard"
	Good  Rating = "good"
	Easy  Rating = "easy"
)

var fsrsRatings = map[Rating]fsrs.Rating{Again: fsrs.Again, Hard: fsrs.Hard, Good: fsrs.Good, Easy: fsrs.Easy}

// ParseRating — оценка из запроса; false — такой оценки нет.
func ParseRating(s string) (Rating, bool) {
	r := Rating(s)
	_, ok := fsrsRatings[r]
	return r, ok
}

// Источники оценки (srs_reviews.source): карточки (S4-3) и ответы cloze
// (S4-5, ADR-037) двигают один и тот же график.
const (
	SourceCard  = "card"
	SourceCloze = "cloze"
)

// State — этап карточки (srs_cards.state).
type State string

const (
	StateNew        State = "new"
	StateLearning   State = "learning"
	StateReview     State = "review"
	StateRelearning State = "relearning"
)

// states — состояния go-fsrs по номеру: New, Learning, Review, Relearning.
var states = [...]State{
	fsrs.New: StateNew, fsrs.Learning: StateLearning, fsrs.Review: StateReview, fsrs.Relearning: StateRelearning,
}

func (st State) fsrsState() (fsrs.State, bool) {
	for i, s := range states {
		if s == st {
			return fsrs.State(i), true
		}
	}
	return 0, false
}

// Card — состояние карточки FSRS, строка srs_cards без ключа (user_id,
// term_id).
type Card struct {
	Due        time.Time
	Stability  float64
	Difficulty float64
	// ScheduledDays — интервал последнего планирования в днях; 0 — шаг
	// обучения в минутах
	ScheduledDays int
	Reps          int
	Lapses        int
	State         State
	// LastReview — nil у карточки без повторений
	LastReview *time.Time
	// RemainingSteps — сколько шагов обучения (минуты) осталось до
	// перехода в review
	RemainingSteps int
}

// NewCard — карточка только что добавленного слова: без повторений
// и к показу сразу (как fsrs.NewCard).
func NewCard(now time.Time) Card {
	return Card{Due: now, State: StateNew}
}

// Scheduler — планировщик с параметрами стенда. Один на процесс:
// go-fsrs копирует параметры на каждый расчёт, параллельные вызовы
// друг другу не мешают.
type Scheduler struct {
	f *fsrs.FSRS
}

// NewScheduler — умолчания go-fsrs (FSRS-6: веса по умолчанию,
// желаемая вероятность вспомнить 0,9, шаги обучения 1 и 10 минут,
// переобучения — 10 минут, потолок интервала 100 лет) без fuzz: интервал
// после оценки предсказуем (DoD S4-3), а случайный разброс дат нужен
// колодам на тысячи карточек в день, не нашим десяткам (ADR-036).
func NewScheduler() *Scheduler {
	p := fsrs.DefaultParam()
	p.EnableFuzz = false
	return &Scheduler{f: fsrs.NewFSRS(p)}
}

// Review — карточка после оценки r в момент now. Ошибка — карточка
// не из этой схемы (неизвестное состояние, прошлое повторение позже now):
// записывать такой результат нельзя.
func (s *Scheduler) Review(c Card, r Rating, now time.Time) (Card, error) {
	grade, ok := fsrsRatings[r]
	if !ok {
		return Card{}, fmt.Errorf("srs: unknown rating %q", r)
	}
	fc, err := c.toFSRS()
	if err != nil {
		return Card{}, err
	}
	info, err := s.f.Next(fc, now, grade)
	if err != nil {
		return Card{}, fmt.Errorf("srs: %w", err)
	}
	return fromFSRS(info.Card), nil
}

func (c Card) toFSRS() (fsrs.Card, error) {
	state, ok := c.State.fsrsState()
	if !ok {
		return fsrs.Card{}, fmt.Errorf("srs: unknown card state %q", c.State)
	}
	fc := fsrs.Card{
		Due: c.Due, Stability: c.Stability, Difficulty: c.Difficulty, State: state, RemainingSteps: c.RemainingSteps,
	}
	if c.ScheduledDays < 0 || c.Reps < 0 || c.Lapses < 0 {
		return fsrs.Card{}, fmt.Errorf("srs: negative counters in card %+v", c)
	}
	fc.ScheduledDays, fc.Reps, fc.Lapses = uint64(c.ScheduledDays), uint64(c.Reps), uint64(c.Lapses)
	if c.LastReview != nil {
		fc.LastReview = *c.LastReview
	}
	return fc, nil
}

func fromFSRS(fc fsrs.Card) Card {
	c := Card{
		Due: fc.Due, Stability: fc.Stability, Difficulty: fc.Difficulty,
		ScheduledDays: int(fc.ScheduledDays), Reps: int(fc.Reps), Lapses: int(fc.Lapses),
		State: states[fc.State], RemainingSteps: fc.RemainingSteps,
	}
	if !fc.LastReview.IsZero() {
		last := fc.LastReview
		c.LastReview = &last
	}
	return c
}

// Пороги статуса по интервалу карточки в review, дни: с недели — «почти
// знаю», с месяца — «знаю».
const (
	almostKnownDays = 7
	knownDays       = 30
)

// Status — статус слова в личном словаре (1..5: новое, узнаю, учу, почти
// знаю, знаю), который повторение ставит по карточке (ADR-036): без
// повторений — 1, на шагах обучения и переобучения — 2, в review — по
// интервалу: до недели 3, до месяца 4, дальше 5. Забытое слово (again
// в review) уходит в переобучение и снова получает 2.
func Status(c Card) int {
	switch {
	case c.State == StateNew:
		return 1
	case c.State != StateReview:
		return 2
	case c.ScheduledDays >= knownDays:
		return 5
	case c.ScheduledDays >= almostKnownDays:
		return 4
	default:
		return 3
	}
}
