package harvester

import (
	"os"
	"reflect"
	"testing"

	"github.com/IEZhu/class/backend/internal/store"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func expectSegments(t *testing.T, got, want []store.SubtitleSegment) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("segments\n got %+v\nwant %+v", got, want)
	}
}

// Ручные субтитры: сегмент на реплику, строки склеены, идентификаторы,
// NOTE, настройки реплик, разметка, пометки и дубли убраны.
func TestParseVTTManual(t *testing.T) {
	got, err := ParseVTT(readFixture(t, "manual.en.vtt"), false)
	if err != nil {
		t.Fatal(err)
	}
	expectSegments(t, got, []store.SubtitleSegment{
		{StartMS: 0, EndMS: 3180, Text: "So I want to start by offering you a small idea about how we listen,"},
		{StartMS: 3180, EndMS: 5940, Text: "and all it asks of you is this:"},
		{StartMS: 7660, EndMS: 12000, Text: "that you stop running for two minutes every day."},
		{StartMS: 12000, EndMS: 15500, Text: "Cats & dogs <both> agree."},
	})
}

// Автосубтитры YouTube «катятся»: каждая строка попадает в корпус один раз,
// с таймингом реплики, где появилась; словные таймкоды и [Music] убраны.
func TestParseVTTRollingAutoCaptions(t *testing.T) {
	got, err := ParseVTT(readFixture(t, "auto.en.vtt"), true)
	if err != nil {
		t.Fatal(err)
	}
	expectSegments(t, got, []store.SubtitleSegment{
		{StartMS: 160, EndMS: 2230, Text: "hello everyone and welcome"},
		{StartMS: 2240, EndMS: 4710, Text: "to the cats running"},
		{StartMS: 7010, EndMS: 9500, Text: "so let's begin"},
	})
}

// Пауза (реплика из пометки) между двумя репликами с той же строкой
// не делает старую строку новой.
func TestParseVTTRollingSurvivesEmptyCue(t *testing.T) {
	data := "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nfirst line\n\n" +
		"00:00:02.000 --> 00:00:02.010\n[Music]\n\n" +
		"00:00:02.010 --> 00:00:04.000\nfirst line\nsecond<00:00:03.000><c> line</c>\n"
	got, err := ParseVTT([]byte(data), true)
	if err != nil {
		t.Fatal(err)
	}
	expectSegments(t, got, []store.SubtitleSegment{
		{StartMS: 1000, EndMS: 2000, Text: "first line"},
		{StartMS: 2010, EndMS: 4000, Text: "second line"},
	})
}

// Без дедупликации те же автосубтитры дали бы повторы — она и нужна.
func TestParseVTTAutoCaptionsWithoutRollingRepeatLines(t *testing.T) {
	got, err := ParseVTT(readFixture(t, "auto.en.vtt"), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) <= 3 {
		t.Fatalf("expected repeated lines without rolling de-duplication, got %+v", got)
	}
}

func TestParseVTTFormats(t *testing.T) {
	data := "\ufeffWEBVTT\r\n\r\nSTYLE\r\n::cue { color: lime }\r\n\r\n" +
		"01:02:03.004 --> 01:02:05.000\r\nlong &nbsp;video\r\n\r\n" +
		"02:03.500 --> 02:04.000\r\n<v Anna>short form\r\n\r\n" +
		"00:00:09.000 --> 00:00:08.000\r\nends before it starts\r\n"
	got, err := ParseVTT([]byte(data), false)
	if err != nil {
		t.Fatal(err)
	}
	expectSegments(t, got, []store.SubtitleSegment{
		{StartMS: 3723004, EndMS: 3725000, Text: "long video"},
		{StartMS: 123500, EndMS: 124000, Text: "short form"},
	})
}

func TestParseVTTRejectsGarbage(t *testing.T) {
	for name, data := range map[string]string{
		"no header":     "00:00:01.000 --> 00:00:02.000\nhello\n",
		"bad timestamp": "WEBVTT\n\n00:00:1.000 --> 00:00:02.000\nhello\n",
		"bad seconds":   "WEBVTT\n\n00:00:61.000 --> 00:01:02.000\nhello\n",
		"no end":        "WEBVTT\n\n00:00:01.000 -->\nhello\n",
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := ParseVTT([]byte(data), false); err == nil {
				t.Fatalf("accepted %q as %+v", data, got)
			}
		})
	}
}
