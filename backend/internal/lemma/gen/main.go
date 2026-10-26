// Команда gen пересобирает встроенные данные лемматизатора (ADR-017) из
// релиза Open English WordNet в формате WNDB:
//
//	curl -LO https://github.com/globalwordnet/english-wordnet/releases/download/2025-edition/english-wordnet-2025.zip
//	go run ./internal/lemma/gen -zip english-wordnet-2025.zip -out internal/lemma/oewn
//
// Из index.{noun,verb,adj,adv} берутся однословные леммы с частями речи
// и числом синсетов (по нему лемматизатор выбирает между кандидатами),
// из *.exc — неправильные формы. Многословные выражения (через «_»)
// отбрасываются: клик приходит по одному слову. Выход детерминирован:
// строки отсортированы, в gzip-заголовке нет времени и имени файла.
package main

import (
	"archive/zip"
	"bufio"
	"compress/gzip"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// posFiles — файлы WNDB и буква части речи, которой их помечает
// встроенный словарь.
var posFiles = []struct{ name, letter string }{
	{"noun", "n"}, {"verb", "v"}, {"adj", "a"}, {"adv", "r"},
}

func main() {
	zipPath := flag.String("zip", "", "релиз OEWN в формате WNDB (english-wordnet-YYYY.zip)")
	out := flag.String("out", "internal/lemma/oewn", "каталог встроенных данных")
	flag.Parse()
	if *zipPath == "" {
		log.Fatal("gen: -zip обязателен")
	}
	zr, err := zip.OpenReader(*zipPath)
	if err != nil {
		log.Fatalf("gen: %v", err)
	}
	defer zr.Close()

	type lemmaInfo struct {
		pos    string
		senses int
	}
	lemmas := map[string]*lemmaInfo{}
	var exceptions []string
	for _, pf := range posFiles {
		err := eachLine(zr, "index."+pf.name, func(line string) error {
			if strings.HasPrefix(line, " ") {
				return nil // шапка с лицензией
			}
			f := strings.Fields(line)
			if len(f) < 3 || strings.Contains(f[0], "_") {
				return nil
			}
			senses, err := strconv.Atoi(f[2])
			if err != nil {
				return fmt.Errorf("index.%s: %q: %w", pf.name, line, err)
			}
			li := lemmas[f[0]]
			if li == nil {
				li = &lemmaInfo{}
				lemmas[f[0]] = li
			}
			li.pos += pf.letter
			li.senses += senses
			return nil
		})
		if err != nil {
			log.Fatalf("gen: %v", err)
		}
		err = eachLine(zr, pf.name+".exc", func(line string) error {
			f := strings.Fields(line)
			if len(f) < 2 || strings.Contains(line, "_") {
				return nil
			}
			exceptions = append(exceptions, pf.letter+"\t"+f[0]+"\t"+strings.Join(f[1:], " "))
			return nil
		})
		if err != nil {
			log.Fatalf("gen: %v", err)
		}
	}

	rows := make([]string, 0, len(lemmas))
	for w, li := range lemmas {
		rows = append(rows, fmt.Sprintf("%s\t%s\t%d", w, li.pos, li.senses))
	}
	sort.Strings(rows)
	sort.Strings(exceptions)
	if err := writeGzip(filepath.Join(*out, "lemmas.tsv.gz"), rows); err != nil {
		log.Fatalf("gen: %v", err)
	}
	if err := writeGzip(filepath.Join(*out, "exceptions.tsv.gz"), exceptions); err != nil {
		log.Fatalf("gen: %v", err)
	}
	log.Printf("gen: %d лемм, %d неправильных форм", len(rows), len(exceptions))
}

// eachLine вызывает fn для каждой строки файла name из любого каталога
// архива (релизы кладут их в oewnYYYY/).
func eachLine(zr *zip.ReadCloser, name string, fn func(string) error) error {
	for _, f := range zr.File {
		if path.Base(f.Name) != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		defer rc.Close()
		sc := bufio.NewScanner(rc)
		sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
		for sc.Scan() {
			if err := fn(sc.Text()); err != nil {
				return err
			}
		}
		return sc.Err()
	}
	return fmt.Errorf("в архиве нет %s", name)
}

func writeGzip(file string, rows []string) error {
	f, err := os.Create(file)
	if err != nil {
		return err
	}
	zw, err := gzip.NewWriterLevel(f, gzip.BestCompression)
	if err != nil {
		f.Close()
		return err
	}
	w := bufio.NewWriter(zw)
	for _, r := range rows {
		if _, err := io.WriteString(w, r+"\n"); err != nil {
			f.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
