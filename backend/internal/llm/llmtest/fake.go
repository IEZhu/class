// Package llmtest — поддельный llm.Provider для тестов: отдаёт
// заготовленные ответы по очереди и запоминает запросы. Настоящий
// провайдер тестам недоступен: ключей нет, а вызов стоит денег.
package llmtest

import (
	"context"
	"sync"
	"time"

	"github.com/IEZhu/class/backend/internal/llm"
)

const (
	Name  = "fake"
	Model = "fake-model"
)

// Reply — один ответ: текст или ошибка вызова. Delay — сколько
// «думает» провайдер; отмена контекста обрывает ожидание его ошибкой.
type Reply struct {
	Text  string
	Err   error
	Delay time.Duration
}

type Fake struct {
	mu       sync.Mutex
	replies  []Reply
	requests []llm.Request
}

// New — провайдер с ответами по очереди; последний повторяется.
func New(replies ...Reply) *Fake { return &Fake{replies: replies} }

// Texts — провайдер, который отвечает этими текстами по очереди.
func Texts(texts ...string) *Fake {
	f := &Fake{}
	for _, t := range texts {
		f.replies = append(f.replies, Reply{Text: t})
	}
	return f
}

func (f *Fake) Name() string  { return Name }
func (f *Fake) Model() string { return Model }

func (f *Fake) Generate(ctx context.Context, req llm.Request) (llm.Response, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	var r Reply
	if len(f.replies) > 0 {
		r = f.replies[min(len(f.requests), len(f.replies))-1]
	} else {
		r.Err = &llm.Error{Provider: Name, Kind: llm.ErrEmpty}
	}
	f.mu.Unlock()
	select {
	case <-time.After(r.Delay):
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	}
	if r.Err != nil {
		return llm.Response{}, r.Err
	}
	return llm.Response{Text: r.Text, Model: Model}, nil
}

// Requests — запросы в порядке вызовов.
func (f *Fake) Requests() []llm.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]llm.Request(nil), f.requests...)
}
