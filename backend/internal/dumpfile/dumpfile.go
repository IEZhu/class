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
	if strings.HasPrefix(source, "http://") {
		return nil, "", fmt.Errorf("только https: дамп не должен подменяться по дороге")
	}
	if !strings.HasPrefix(source, "https://") {
		f, err := os.Open(source)
		if err != nil {
			return nil, "", err
		}
		fi, err := f.Stat()
		if err != nil {
			f.Close()
			return nil, "", err
		}
		return f, fmt.Sprintf("size=%d mtime=%d", fi.Size(), fi.ModTime().Unix()), nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, "", err
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
		return nil, "", err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, "", fmt.Errorf("GET %s: HTTP %d", source, resp.StatusCode)
	}
	version := resp.Header.Get("ETag")
	if version == "" {
		version = resp.Header.Get("Last-Modified")
	}
	return resp.Body, version, nil
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
