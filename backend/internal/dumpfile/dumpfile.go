// Package dumpfile — источники разовых импортов открытых дампов
// (словарь S3-4, ADR-019; корпус Tatoeba S5-4, ADR-028): файл по https
// или с диска, распаковка по сигнатуре и построчное чтение, в котором
// недокачанный файл отличим от конца дампа.
package dumpfile

import (
	"bufio"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// Open открывает источник и возвращает его версию: по ней импорт решает,
// можно ли продолжить незавершённый прогон (ETag или Last-Modified у URL,
// размер и время изменения у файла).
func Open(ctx context.Context, source string) (io.ReadCloser, string, error) {
	isURL, err := checkSource(source)
	if err != nil {
		return nil, "", err
	}
	if !isURL {
		f, err := os.Open(source)
		if err != nil {
			return nil, "", err
		}
		fi, err := f.Stat()
		if err != nil {
			f.Close()
			return nil, "", err
		}
		return f, fileVersion(fi), nil
	}
	resp, err := fetch(ctx, http.MethodGet, source)
	if err != nil {
		return nil, "", err
	}
	return resp.Body, httpVersion(resp.Header), nil
}

// Stat — версия источника без чтения: HEAD у URL, stat у файла.
func Stat(ctx context.Context, source string) (string, error) {
	isURL, err := checkSource(source)
	if err != nil {
		return "", err
	}
	if !isURL {
		fi, err := os.Stat(source)
		if err != nil {
			return "", err
		}
		return fileVersion(fi), nil
	}
	resp, err := fetch(ctx, http.MethodHead, source)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	return httpVersion(resp.Header), nil
}

// checkSource — источник по https (true) или с диска; http не принимается.
func checkSource(source string) (bool, error) {
	if strings.HasPrefix(source, "http://") {
		return false, fmt.Errorf("только https: дамп не должен подменяться по дороге")
	}
	return strings.HasPrefix(source, "https://"), nil
}

func fileVersion(fi os.FileInfo) string {
	return fmt.Sprintf("size=%d mtime=%d", fi.Size(), fi.ModTime().Unix())
}

func httpVersion(h http.Header) string {
	if v := h.Get("ETag"); v != "" {
		return v
	}
	return h.Get("Last-Modified")
}

// fetch — запрос к источнику по https; ответ не 200 — ошибка.
func fetch(ctx context.Context, method, source string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, source, nil)
	if err != nil {
		return nil, err
	}
	// Без общего таймаута: полный дамп качается минутами. Зависание
	// соединения ловят таймауты установки и первого ответа.
	client := &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   30 * time.Second,
		ResponseHeaderTimeout: time.Minute,
	}}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s %s: HTTP %d", method, source, resp.StatusCode)
	}
	return resp, nil
}

// ErrChanged — источник Lazy к первому чтению сменил версию.
var ErrChanged = errors.New("dumpfile: источник сменился после начала импорта")

// Lazy — источник, который открывается при первом чтении, а версию знает
// заранее (Stat). Импорт из нескольких файлов читает их по очереди, и
// соединение, которое ждёт своей очереди непрочитанным, сервер выгрузок
// может закрыть по таймауту посреди файла. Сменилась версия к открытию — чтение вернёт
// ErrChanged: контрольная точка прогона относится к прежнему файлу.
type Lazy struct {
	ctx             context.Context
	source, version string
	rc              io.ReadCloser
	err             error
}

// OpenLazy узнаёт версию источника; открывает его первое чтение.
func OpenLazy(ctx context.Context, source string) (*Lazy, error) {
	version, err := Stat(ctx, source)
	if err != nil {
		return nil, err
	}
	return &Lazy{ctx: ctx, source: source, version: version}, nil
}

// Version — версия источника на момент OpenLazy.
func (l *Lazy) Version() string { return l.version }

func (l *Lazy) Read(p []byte) (int, error) {
	if l.rc == nil && l.err == nil {
		rc, version, err := Open(l.ctx, l.source)
		switch {
		case err != nil:
			l.err = err
		case version != l.version:
			rc.Close()
			l.err = fmt.Errorf("%w: %s (была версия %q, теперь %q)", ErrChanged, l.source, l.version, version)
		default:
			l.rc = rc
		}
	}
	if l.err != nil {
		return 0, l.err
	}
	return l.rc.Read(p)
}

// Close закрывает источник, если его успели открыть.
func (l *Lazy) Close() error {
	if l.rc == nil {
		return nil
	}
	return l.rc.Close()
}

// Decompress распознаёт gzip и bzip2 по сигнатуре: kaikki.org отдаёт
// .jsonl и .jsonl.gz, Tatoeba — .tsv.bz2, а у локального файла
// расширение может быть любым.
func Decompress(src io.Reader) (io.Reader, error) {
	br := bufio.NewReader(src)
	magic, err := br.Peek(3)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	switch {
	case bytes.HasPrefix(magic, []byte{0x1f, 0x8b}):
		return gzip.NewReader(br)
	case bytes.Equal(magic, []byte("BZh")):
		return bzip2.NewReader(br), nil
	}
	return br, nil
}

// ReadLine — следующая строка без перевода строки. Строка длиннее max
// байт дочитывается впустую и помечается tooLong. Последняя строка без
// перевода строки — обрыв источника (недокачанный файл), а не конец
// дампа: такой прогон не должен завершиться и удалить данные, до которых
// не дошёл.
func ReadLine(br *bufio.Reader, max int) (line []byte, tooLong bool, err error) {
	var buf []byte
	for {
		chunk, err := br.ReadSlice('\n')
		if !tooLong && len(buf)+len(chunk) > max {
			tooLong, buf = true, nil
		}
		if !tooLong {
			buf = append(buf, chunk...)
		}
		switch {
		case err == nil:
			return bytes.TrimSuffix(buf, []byte("\n")), tooLong, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(buf) == 0 && !tooLong:
			return nil, false, io.EOF
		case errors.Is(err, io.EOF):
			return nil, false, io.ErrUnexpectedEOF
		default:
			return nil, false, err
		}
	}
}
