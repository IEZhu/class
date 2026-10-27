package textgen

import (
	"context"
	"fmt"
	"strings"

	"github.com/IEZhu/class/backend/internal/llm"
)

// maxAttempts — черновик и один ретрай с перечнем недостающих слов
// (integrations/llm.md): третий вызов редко спасает то, что не вышло
// со второго, а стоит как первые два.
const maxAttempts = 2

// Result — текст, прошедший валидатор.
type Result struct {
	// Text — абзацы через пустую строку
	Text     string
	Report   Report
	Attempts int
	Provider string
	Model    string
}

// CoverageError — ни черновик, ни ретрай не прошли валидатор. Report —
// лучший из них: что сказать пользователю о недостающих словах.
type CoverageError struct{ Report Report }

func (e *CoverageError) Error() string {
	return fmt.Sprintf("textgen: покрытие %.2f после %d попыток, не хватает: %s",
		e.Report.Coverage, maxAttempts, strings.Join(e.Report.Missing, ", "))
}

// Generate пишет текст провайдером p и проверяет покрытие. Текст ниже
// порога — один ретрай с недостающими словами; сбой вызова возвращается
// как есть (*llm.Error), провал обеих попыток — *CoverageError.
func Generate(ctx context.Context, p llm.Provider, params Params) (*Result, error) {
	byLemma := make(map[string]Target, len(params.Targets))
	for _, t := range params.Targets {
		byLemma[t.Lemma] = t
	}
	var (
		best    Report
		missing []Target
	)
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, err := p.Generate(ctx, buildRequest(params, missing))
		if err != nil {
			return nil, err
		}
		text := paragraphs(resp.Text)
		rep := Check(text, params.Targets)
		if rep.Passed() {
			return &Result{Text: text, Report: rep, Attempts: attempt, Provider: p.Name(), Model: resp.Model}, nil
		}
		if attempt == 1 || rep.Coverage > best.Coverage {
			best = rep
		}
		missing = missing[:0]
		for _, lem := range rep.Missing {
			missing = append(missing, byLemma[lem])
		}
	}
	return nil, &CoverageError{Report: best}
}

// paragraphs — текст абзацами через пустую строку: модели разделяют
// абзацы то одним, то двумя переводами строки, а реплики диалога —
// одним; каждая непустая строка — абзац.
func paragraphs(text string) string {
	var out []string
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n\n")
}
