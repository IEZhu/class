package harvester

import (
	"errors"
	"fmt"
	"html"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/IEZhu/class/backend/internal/store"
)

var (
	errNotVTT = errors.New("vtt: нет заголовка WEBVTT")

	// Теги разметки реплики: <c>, <i>, <v Имя>, словные таймкоды
	// автосубтитров <00:00:01.230>
	cueTag = regexp.MustCompile(`<[^>]*>`)
	// Звуковые пометки: [Music], [Applause]
	soundNote = regexp.MustCompile(`\[[^\]]*\]`)
	// Реплика целиком из пометки в скобках: (Laughter), (Applause)
	onlyParenthetical = regexp.MustCompile(`^\([^)]*\)$`)
)

// ParseVTT разбирает WebVTT в сегменты субтитров: теги, звуковые пометки
// и сущности HTML убираются, пробелы схлопываются, пустые реплики
// пропускаются.
//
// rolling — автосубтитры YouTube: каждая реплика повторяет предыдущую
// строку и дописывает новую, между ними идут реплики по 10 мс с одной
// старой строкой. Тогда сегмент — строка, которой не было в предыдущей
// реплике, с таймингом той реплики, где она появилась. Ручные субтитры
// (rolling = false) — сегмент на реплику, строки склеиваются; подряд
// идущие одинаковые реплики сливаются в одну.
func ParseVTT(data []byte, rolling bool) ([]store.SubtitleSegment, error) {
	text := strings.TrimPrefix(string(data), "\ufeff")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	if !strings.HasPrefix(text, "WEBVTT") {
		return nil, errNotVTT
	}
	var segments []store.SubtitleSegment
	var previous []string // строки предыдущей реплики (rolling)
	// Первый блок — заголовок файла (WEBVTT, Kind:, Language:)
	for _, block := range strings.Split(text, "\n\n")[1:] {
		lines := strings.Split(strings.Trim(block, "\n"), "\n")
		timing := slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, "-->") })
		// NOTE, STYLE и REGION таймингов не содержат
		if timing < 0 {
			continue
		}
		start, end, err := parseTiming(lines[timing])
		if err != nil {
			return nil, err
		}
		if end < start {
			continue
		}
		var payload []string
		for _, line := range lines[timing+1:] {
			if clean := cleanCueText(line); clean != "" {
				payload = append(payload, clean)
			}
		}
		if rolling {
			for _, line := range payload {
				if !slices.Contains(previous, line) {
					segments = append(segments, store.SubtitleSegment{StartMS: start, EndMS: end, Text: line})
				}
			}
			// Пустая реплика (пауза, одна пометка) строку на экране не меняет
			if len(payload) > 0 {
				previous = payload
			}
			continue
		}
		joined := strings.Join(payload, " ")
		if joined == "" {
			continue
		}
		if n := len(segments); n > 0 && segments[n-1].Text == joined {
			segments[n-1].EndMS = max(segments[n-1].EndMS, end)
			continue
		}
		segments = append(segments, store.SubtitleSegment{StartMS: start, EndMS: end, Text: joined})
	}
	return segments, nil
}

// cleanCueText — строка реплики как чистый текст; "" — нечего сохранять.
func cleanCueText(line string) string {
	line = cueTag.ReplaceAllString(line, "")
	line = html.UnescapeString(line)
	line = soundNote.ReplaceAllString(line, " ")
	line = strings.Map(func(r rune) rune {
		// ♪ и невидимые символы форматирования (&lrm;, нулевой ширины)
		if r == '♪' || r == '♫' || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, line)
	line = strings.Join(strings.Fields(line), " ")
	if onlyParenthetical.MatchString(line) {
		return ""
	}
	return line
}

// parseTiming разбирает строку «начало --> конец [настройки]» в миллисекунды.
func parseTiming(line string) (int, int, error) {
	fields := strings.Fields(line)
	if len(fields) < 3 || fields[1] != "-->" {
		return 0, 0, fmt.Errorf("vtt: строка тайминга %q", line)
	}
	start, err := parseTimestamp(fields[0])
	if err != nil {
		return 0, 0, err
	}
	end, err := parseTimestamp(fields[2])
	if err != nil {
		return 0, 0, err
	}
	return start, end, nil
}

// parseTimestamp: [чч:]мм:сс.ттт → миллисекунды.
func parseTimestamp(ts string) (int, error) {
	bad := fmt.Errorf("vtt: метка времени %q", ts)
	clock, frac, ok := strings.Cut(strings.Replace(ts, ",", ".", 1), ".")
	if !ok || len(frac) != 3 {
		return 0, bad
	}
	parts := strings.Split(clock, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, bad
	}
	ms, err := strconv.Atoi(frac)
	if err != nil || ms < 0 {
		return 0, bad
	}
	total := 0
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		// Часы — от двух цифр, минуты и секунды — ровно две, до 59
		hours := len(parts) == 3 && i == 0
		if err != nil || n < 0 || len(part) < 2 || (!hours && (len(part) != 2 || n > 59)) {
			return 0, bad
		}
		total = total*60 + n
	}
	return total*1000 + ms, nil
}
