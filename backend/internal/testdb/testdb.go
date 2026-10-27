// Package testdb — общий harness тестов на одноразовой базе Postgres из
// TEST_DATABASE_URL: у каждого теста своя схема с настоящими миграциями.
//
// go test ./... гоняет пакеты параллельно на одной базе, поэтому миграции
// накатываются под advisory-локом, а pgcrypto — расширение одно на всю
// базу — заранее создаётся в public. Иначе два пакета одновременно
// выполняют CREATE EXTENSION из 0002 и один падает с 23505, а расширение
// оказывается в схеме теста и уходит вместе с ней.
package testdb

import (
	"context"
	"crypto/rand"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// migrateLock — ключ pg_advisory_lock на время наката миграций. Число
// произвольное, лишь бы не совпало с чужими локами в тестовой базе.
const migrateLock int64 = 0x6c696e677561 // "lingua"

// migrations — каталог backend/migrations. Тесты запускаются из каталога
// своего пакета, поэтому путь считается от этого файла, а не от рабочего.
var migrations = func() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "migrations")
}()

// Execer — то, на чём выполняется файл миграции: соединение, пул, транзакция.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// DSN создаёт схему, накатывает в неё все *.up.sql по порядку и возвращает
// строку подключения с search_path на неё; схема удаляется в t.Cleanup.
// Без TEST_DATABASE_URL тест пропускается, а в CI падает: CI требует базу,
// а не пропускает молча проверки границ доступа.
func DSN(t testing.TB) string {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_DATABASE_URL is required in CI")
		}
		t.Skip("set TEST_DATABASE_URL to a disposable Postgres database")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(ctx) })

	// Лок сессионный, поэтому его держит отдельное соединение admin, а не
	// пул; defer отпускает его и после t.Fatal.
	if _, err := admin.Exec(ctx, "SELECT pg_advisory_lock($1)", migrateLock); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := admin.Exec(ctx, "SELECT pg_advisory_unlock($1)", migrateLock); err != nil {
			t.Errorf("unlock migrations: %v", err)
		}
	}()
	// После этого IF NOT EXISTS из 0002 ничего не делает. Новое расширение
	// в миграциях создавать здесь же: иначе его унесёт DROP SCHEMA теста
	if _, err := admin.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS pgcrypto SCHEMA public"); err != nil {
		t.Fatal(err)
	}
	schema := "test_" + strings.ToLower(rand.Text()[:16])
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("clean test schema: %v", err)
		}
	})

	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	conn, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	files, err := filepath.Glob(filepath.Join(migrations, "*.up.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("migration inventory: %v (%d files)", err, len(files))
	}
	sort.Strings(files)
	for _, file := range files {
		Apply(t, conn, filepath.Base(file))
	}
	return u.String()
}

// Apply выполняет на db файл миграции по имени, например "0007_jobs.down.sql".
func Apply(t testing.TB, db Execer, name string) {
	t.Helper()
	sql, err := os.ReadFile(filepath.Join(migrations, name))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(context.Background(), string(sql)); err != nil {
		t.Fatalf("migration %s: %v", name, err)
	}
}
