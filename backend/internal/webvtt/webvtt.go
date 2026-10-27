// Package webvtt рендерит реплики транскрипта в WebVTT
// (https://www.w3.org/TR/webvtt1/): одна реплика — один cue, спикер —
// voice span `<v A>`. Файл ложится в S3 рядом с записью и подключается
// к плееру как <track> (S2-3).
package webvtt

import (
	"bytes"
	"cmp"
	"fmt"
	"slices"
	"strings"
)

// ContentType — с ним браузер принимает файл как <track>.
const ContentType = "text/vtt; charset=utf-8"

// Cue — одна реплика; время — миллисекунды от начала записи.
type Cue struct {
	StartMS int64
	EndMS   int64
	Voice   string
	Text    string
}

// Render собирает файл WebVTT. Спецификация требует, чтобы cue шли по
// неубыванию начала и кончались строго позже, чем начались, а в тексте
// не было пустых строк и «-->»: Render упорядочивает cue (устойчиво,
// при равном начале — в исходном порядке), растягивает нулевые до 1 мс,
// сводит переводы строк к пробелу и экранирует &, < и >.
func Render(cues []Cue) []byte {
	sorted := slices.Clone(cues)
	slices.SortStableFunc(sorted, func(a, b Cue) int { return cmp.Compare(a.StartMS, b.StartMS) })

	var b bytes.Buffer
	b.WriteString("WEBVTT\n")
	for _, c := range sorted {
		start := max(c.StartMS, 0)
		end := max(c.EndMS, start+1)
		fmt.Fprintf(&b, "\n%s --> %s\n", timestamp(start), timestamp(end))
		if voice := annotation(c.Voice); voice != "" {
			fmt.Fprintf(&b, "<v %s>", voice)
		}
		b.WriteString(escape(oneLine(c.Text)))
		b.WriteString("\n")
	}
	return b.Bytes()
}

// timestamp — HH:MM:SS.mmm; часы всегда, чтобы формат не зависел
// от длины урока.
func timestamp(ms int64) string {
	return fmt.Sprintf("%02d:%02d:%02d.%03d", ms/3_600_000, ms/60_000%60, ms/1000%60, ms%1000)
}

// oneLine — cue кончается на первой пустой строке, поэтому любые
// переводы строк и их серии сводим к одному пробелу.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

var textEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func escape(s string) string { return textEscaper.Replace(s) }

// annotation — имя голоса кончается на «>», поэтому его, как и текст,
// экранируем; переводы строк убираем.
func annotation(s string) string { return escape(oneLine(s)) }
