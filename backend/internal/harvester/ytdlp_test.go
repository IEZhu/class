package harvester

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IEZhu/class/backend/internal/store"
)

// fakeYTDLP — настоящий YTDLP поверх testdata/fake-yt-dlp: проверяется
// вызов процесса, разбор его файлов и ошибок, но в YouTube никто не ходит.
func fakeYTDLP(t *testing.T) (YTDLP, func() []string) {
	t.Helper()
	path, err := filepath.Abs("testdata/fake-yt-dlp")
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	argsFile := filepath.Join(home, "yt-dlp.args")
	// args — аргументы вызовов с прошлого чтения
	return YTDLP{Path: path, Timeout: 5 * time.Second}, func() []string {
		t.Helper()
		data, err := os.ReadFile(argsFile)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(argsFile); err != nil {
			t.Fatal(err)
		}
		return strings.Split(strings.TrimSpace(string(data)), "\n")
	}
}

func TestYTDLPRequestsEnglishManualAndAutoWithoutDownload(t *testing.T) {
	y, args := fakeYTDLP(t)
	subs, err := y.Fetch(context.Background(), "manualSubs1")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(args(), " ")
	for _, want := range []string{"--ignore-config", "--skip-download --write-subs --write-auto-subs --sub-langs en",
		"--sub-format vtt", "--write-info-json", "-- https://www.youtube.com/watch?v=manualSubs1"} {
		if !strings.Contains(got, want) {
			t.Errorf("args %q lack %q", got, want)
		}
	}
	if strings.Contains(got, "--cookies") {
		t.Errorf("cookies passed without a cookies file: %q", got)
	}
	if subs.Kind != store.SubtitlesManual || subs.Duration != 754 || !strings.HasPrefix(string(subs.VTT), "WEBVTT") {
		t.Fatalf("subtitles %s, %d s, %d bytes", subs.Kind, subs.Duration, len(subs.VTT))
	}
}

// Ручные субтитры приоритетнее: auto — только когда ручных en нет;
// английских нет вовсе — none, без ошибки.
func TestYTDLPClassifiesSubtitles(t *testing.T) {
	y, _ := fakeYTDLP(t)
	for id, want := range map[string]string{
		"manualSubs1": store.SubtitlesManual,
		"autoSubs001": store.SubtitlesAuto,
		"noSubtitle1": store.SubtitlesNone,
	} {
		subs, err := y.Fetch(context.Background(), id)
		if err != nil || subs.Kind != want {
			t.Errorf("%s: %q, %v; want %q", id, subs.Kind, err, want)
		}
		if (want == store.SubtitlesNone) != (subs.VTT == nil) {
			t.Errorf("%s: %d bytes of VTT", id, len(subs.VTT))
		}
	}
}

func TestYTDLPErrors(t *testing.T) {
	y, _ := fakeYTDLP(t)
	ctx := context.Background()
	for _, id := range []string{"tooMany4291", "botCheck001"} {
		if _, err := y.Fetch(ctx, id); !errors.Is(err, ErrBlocked) {
			t.Errorf("%s: %v, want ErrBlocked", id, err)
		}
	}
	_, err := y.Fetch(ctx, "private0001")
	if err == nil || errors.Is(err, ErrBlocked) || !strings.Contains(err.Error(), "Private video") {
		t.Errorf("private video: %v", err)
	}
	if _, err := y.Fetch(ctx, "../../etc/x"); err == nil {
		t.Error("malformed video id reached yt-dlp")
	}

	slow := y
	slow.Timeout = 200 * time.Millisecond
	start := time.Now()
	if _, err := slow.Fetch(ctx, "slowVideo01"); err == nil || !strings.Contains(err.Error(), "не уложился") {
		t.Errorf("slow call: %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("timeout took %s", time.Since(start))
	}

	missing := YTDLP{Path: filepath.Join(t.TempDir(), "yt-dlp")}
	if _, err := missing.Fetch(ctx, "manualSubs1"); !errors.Is(err, ErrNoRunner) {
		t.Errorf("missing binary: %v, want ErrNoRunner", err)
	}
}

// Пароль базы из окружения worker'а до yt-dlp и кода плеера не доходит.
func TestYTDLPDoesNotLeakWorkerEnvironment(t *testing.T) {
	y, _ := fakeYTDLP(t)
	t.Setenv("DATABASE_URL", "postgres://lingua:fixture-password@postgres/lingua")
	if subs, err := y.Fetch(context.Background(), "envCheck001"); err != nil || subs.Kind != store.SubtitlesNone {
		t.Fatalf("Fetch = %+v, %v", subs, err)
	}
}

// Cookies уходят копией в каталог вызова; пустой файл — как без cookies.
func TestYTDLPCookies(t *testing.T) {
	y, args := fakeYTDLP(t)
	jar := filepath.Join(t.TempDir(), "cookies.txt")
	if err := os.WriteFile(jar, []byte("# Netscape HTTP Cookie File\n.youtube.com\tTRUE\t/\tTRUE\t0\tPREF\tf1=1\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	y.Cookies = jar
	if _, err := y.Fetch(context.Background(), "cookieJar01"); err != nil {
		t.Fatal(err)
	}
	if got := args(); !slices.Contains(got, "--cookies") || slices.Contains(got, jar) {
		t.Fatalf("args %q: want a copy of the jar", got)
	}

	if err := os.WriteFile(filepath.Join(filepath.Dir(jar), "empty.txt"), nil, 0o400); err != nil {
		t.Fatal(err)
	}
	y.Cookies = filepath.Join(filepath.Dir(jar), "empty.txt")
	if _, err := y.Fetch(context.Background(), "noSubtitle1"); err != nil {
		t.Fatal(err)
	}
	if got := args(); slices.Contains(got, "--cookies") {
		t.Fatalf("empty jar passed: %q", got)
	}

	y.Cookies = filepath.Join(filepath.Dir(jar), "absent.txt")
	if _, err := y.Fetch(context.Background(), "noSubtitle1"); err == nil {
		t.Fatal("missing cookies file ignored")
	}
}
