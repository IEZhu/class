// Package llm — LLM-провайдеры за одним интерфейсом (S5-6, ADR-029):
// Anthropic Messages API, OpenAI Responses API и DeepSeek Chat
// Completions. Провайдера выбирает конфиг (LLM_PROVIDER, LLM_API_KEY,
// LLM_MODEL); без ключа New возвращает nil, генерация отвечает 503,
// остальной стенд работает. SDK провайдеров не берём: каждому нужен
// ровно один вызов.
//
// Ключ уходит только в заголовок запроса. В ошибки он не попадает:
// текст ошибки провайдера чистится от вхождений ключа, а у отказа
// авторизации не берётся вовсе (OpenAI повторяет в нём замаскированный
// ключ).
package llm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	ProviderAnthropic = "anthropic"
	ProviderOpenAI    = "openai"
	ProviderDeepSeek  = "deepseek"
)

// Providers — порядок из integrations/llm.md; первый — умолчание
// пустого LLM_PROVIDER.
var Providers = []string{ProviderAnthropic, ProviderOpenAI, ProviderDeepSeek}

// defaultModels — самая дешёвая актуальная модель каждого провайдера
// по его документации на 2026-10 (ADR-029); LLM_MODEL её заменяет.
var defaultModels = map[string]string{
	ProviderAnthropic: "claude-haiku-5-5",
	ProviderOpenAI:    "gpt-6-luna",
	ProviderDeepSeek:  "deepseek-flash",
}

// Timeout — потолок одного вызова. Текст в 400 слов дешёвая модель
// пишет за секунды; генерация с ретраем — два вызова, и вместе они
// должны уложиться в таймаут прокси перед api (ADR-029).
const Timeout = 25 * time.Second

// Request — один вызов модели: инструкции, сообщение пользователя и
// потолок вывода в токенах (вместе с рассуждением модели, если оно есть).
type Request struct {
	System    string
	Prompt    string
	MaxTokens int
}

// Response — текст ответа и модель, которая его написала (по ответу
// провайдера; пусто в ответе — модель из конфига).
type Response struct {
	Text  string
	Model string
}

// Provider — то, что нужно генерации от LLM. Тесты подставляют
// llmtest.Fake.
type Provider interface {
	Name() string
	Model() string
	Generate(ctx context.Context, req Request) (Response, error)
}

type Config struct {
	// Provider — anthropic | openai | deepseek; пусто — Providers[0]
	Provider string
	APIKey   string
	// Model пусто — модель провайдера по умолчанию
	Model string
	// BaseURL пусто — адрес провайдера из его документации; в тестах —
	// fake-сервер
	BaseURL string
}

// ConfigFromEnv — ключи LLM_* из окружения api (deploy/.env.example).
func ConfigFromEnv() Config {
	return Config{
		Provider: os.Getenv("LLM_PROVIDER"),
		APIKey:   os.Getenv("LLM_API_KEY"),
		Model:    os.Getenv("LLM_MODEL"),
	}
}

// New возвращает nil без ошибки, если ключа нет: генерация выключена.
// Неизвестный провайдер — ошибка конфигурации и без ключа: опечатку
// лучше увидеть на старте, а не после того, как ключ появится.
func New(cfg Config) (Provider, error) {
	name := strings.ToLower(strings.TrimSpace(cfg.Provider))
	if name == "" {
		name = Providers[0]
	}
	if _, ok := defaultModels[name]; !ok {
		return nil, fmt.Errorf("llm: LLM_PROVIDER=%q, ждали одно из %s", cfg.Provider, strings.Join(Providers, ", "))
	}
	if cfg.APIKey == "" {
		return nil, nil
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = defaultModels[name]
	}
	c := &client{name: name, model: model, apiKey: cfg.APIKey,
		baseURL: strings.TrimSuffix(cfg.BaseURL, "/"), http: newHTTPClient()}
	switch name {
	case ProviderAnthropic:
		return &anthropic{c}, nil
	case ProviderOpenAI:
		return &openAI{c}, nil
	default:
		return &deepSeek{c}, nil
	}
}

// Виды сбоя вызова (Error.Kind). По ним api выбирает ответ: временные —
// 503 «попробуйте позже», остальные — 502.
var (
	ErrAuth        = errors.New("ключ или оплата отклонены")
	ErrRateLimited = errors.New("лимит запросов провайдера")
	ErrUnavailable = errors.New("провайдер недоступен")
	ErrTimeout     = errors.New("таймаут вызова")
	ErrRejected    = errors.New("запрос отклонён")
	ErrTruncated   = errors.New("ответ обрезан потолком токенов")
	ErrEmpty       = errors.New("пустой ответ")
	ErrTooLarge    = errors.New("ответ больше потолка")
	ErrBadResponse = errors.New("ответ не разобран")
)

// Error — сбой вызова провайдера; errors.Is(err, ErrRateLimited) и т.п.
type Error struct {
	Provider string
	// Status — HTTP-статус ответа; 0 — до ответа не дошло
	Status int
	Kind   error
	// Detail — причина от провайдера: без ключа, не длиннее maxDetailBytes
	Detail string
}

func (e *Error) Error() string {
	msg := "llm " + e.Provider
	if e.Status != 0 {
		msg += fmt.Sprintf(": HTTP %d", e.Status)
	}
	msg += ": " + e.Kind.Error()
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Kind }

// Temporary — сбой пройдёт сам: перегрузка, лимит, таймаут, сеть.
func Temporary(err error) bool {
	return errors.Is(err, ErrRateLimited) || errors.Is(err, ErrUnavailable) || errors.Is(err, ErrTimeout)
}
