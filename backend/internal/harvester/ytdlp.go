package harvester

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/IEZhu/class/backend/internal/store"
)

var (
	// ErrBlocked — YouTube ограничил этот IP: 429, «подтвердите, что вы не
	// бот», rate limit сессии. Сборщик уходит в паузу (Pace.Block).
	ErrBlocked = errors.New("youtube ограничил доступ")
	// ErrNoRunner — yt-dlp не запускается: беда окружения (нет бинаря,
	// не читается файл cookies), а не видео.
	ErrNoRunner = errors.New("yt-dlp не запускается")
)

// Subtitles — английские субтитры одного видео.
type Subtitles struct {
	Kind     string // store.SubtitlesManual | SubtitlesAuto | SubtitlesNone
	VTT      []byte
	Duration int // секунды, 0 — неизвестна
}

// Fetcher достаёт субтитры видео YouTube; в тестах — подделка.
type Fetcher interface {
	Fetch(ctx context.Context, videoID string) (Subtitles, error)
}

// Признаки блокировки в выводе yt-dlp (в нижнем регистре)
var blockMarkers = []string{
	"http error 429", "too many requests", "rate-limited", "rate limited",
	"not a bot", "try again later",
}

// YTDLP запускает yt-dlp: одно видео — один вызов, без скачивания самого
// видео. Ручные и автосубтитры запрашиваются вместе: для одного языка
// yt-dlp сам берёт ручные, а auto — только если ручных нет. Какие вышли,
// видно по info.json: ручные у видео есть в subtitles.
type YTDLP struct {
	// Path — бинарь yt-dlp, по умолчанию из PATH
	Path string
	// Cookies — файл cookies в формате Netscape, по необходимости. Пустой
	// файл — как без cookies. yt-dlp получает копию: он дописывает jar
	// при выходе, а оригинал может быть смонтирован только на чтение.
	Cookies string
	// Timeout — потолок одного вызова, по умолчанию 5 мин
	Timeout time.Duration
}

func (y YTDLP) Fetch(ctx context.Context, id string) (Subtitles, error) {
	if !videoID.MatchString(id) {
		return Subtitles{}, fmt.Errorf("id видео %q", id)
	}
	dir, err := os.MkdirTemp("", "subtitles-")
	if err != nil {
		return Subtitles{}, err
	}
	defer os.RemoveAll(dir)

	args := []string{
		"--ignore-config", "--no-playlist", "--no-progress", "--socket-timeout", "30",
		"--skip-download", "--write-subs", "--write-auto-subs", "--sub-langs", "en", "--sub-format", "vtt",
		"--write-info-json", "-P", dir, "-o", "%(id)s.%(ext)s",
	}
	if y.Cookies != "" {
		jar, err := copyCookies(y.Cookies, dir)
		if err != nil {
			// Файл не читается — одинаково для любого видео: попытки
			// видео на это не тратятся (ADR-022)
			return Subtitles{}, fmt.Errorf("%w: %v", ErrNoRunner, err)
		}
		if jar != "" {
			args = append(args, "--cookies", jar)
		}
	}
	args = append(args, "--", "https://www.youtube.com/watch?v="+id)

	timeout := y.Timeout
	if timeout <= 0 {
		timeout = ytdlpTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	path := y.Path
	if path == "" {
		path = "yt-dlp"
	}
	cmd := exec.CommandContext(cctx, path, args...)
	// yt-dlp и JS-движок исполняют код плеера YouTube: окружение worker'а
	// (DATABASE_URL с паролем базы) им ни к чему. Это лишь слой защиты:
	// процесс того же uid прочтёт окружение worker'а из /proc/1/environ.
	// Настоящие барьеры — node --permission, закреплённое колесо yt-dlp
	// и своя роль базы сборщика с доступом только к его таблицам (ADR-022)
	cmd.Env = ytdlpEnv()
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	// Дочерние процессы (JS-движок) могут держать вывод после убийства yt-dlp
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return Subtitles{}, fmt.Errorf("%w: %v", ErrNoRunner, err)
		}
		// Остановка worker'а — не вина видео
		if ctx.Err() != nil {
			return Subtitles{}, ctx.Err()
		}
		msg := lastLines(out.String(), 3)
		if isBlocked(msg) {
			return Subtitles{}, fmt.Errorf("%w: %s", ErrBlocked, msg)
		}
		if cctx.Err() != nil {
			return Subtitles{}, fmt.Errorf("yt-dlp не уложился в %s: %s", timeout, msg)
		}
		return Subtitles{}, fmt.Errorf("yt-dlp: %s", msg)
	}

	infoJSON, err := os.ReadFile(filepath.Join(dir, id+".info.json"))
	if err != nil {
		return Subtitles{}, fmt.Errorf("yt-dlp не записал info.json: %w", err)
	}
	var info struct {
		Duration  float64                    `json:"duration"`
		Subtitles map[string]json.RawMessage `json:"subtitles"`
	}
	if err := json.Unmarshal(infoJSON, &info); err != nil {
		return Subtitles{}, fmt.Errorf("info.json: %w", err)
	}
	subs := Subtitles{Kind: store.SubtitlesNone, Duration: int(info.Duration)}
	vtt, err := os.ReadFile(filepath.Join(dir, id+".en.vtt"))
	switch {
	case errors.Is(err, os.ErrNotExist):
		// «There are no subtitles for the requested languages»
		return subs, nil
	case err != nil:
		return Subtitles{}, err
	}
	subs.VTT = vtt
	subs.Kind = store.SubtitlesAuto
	if _, manual := info.Subtitles["en"]; manual {
		subs.Kind = store.SubtitlesManual
	}
	return subs, nil
}

// ytdlpEnv — окружение вызова yt-dlp: только то, что нужно ему самому
// (PATH, HOME с кэшем данных плеера YouTube, локаль, прокси).
func ytdlpEnv() []string {
	var env []string
	for _, key := range []string{"PATH", "HOME", "LANG", "LC_ALL", "TMPDIR",
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy"} {
		if v, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+v)
		}
	}
	return env
}

// copyCookies кладёт копию jar в каталог вызова; "" — файл пуст.
func copyCookies(src, dir string) (string, error) {
	data, err := os.ReadFile(src)
	if err != nil {
		return "", fmt.Errorf("cookies: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return "", nil
	}
	jar := filepath.Join(dir, "cookies.txt")
	return jar, os.WriteFile(jar, data, 0o600)
}

func isBlocked(msg string) bool {
	msg = strings.ToLower(strings.ReplaceAll(msg, "’", "'"))
	for _, marker := range blockMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// lastLines — хвост вывода для last_error: там у yt-dlp строка ERROR.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}
