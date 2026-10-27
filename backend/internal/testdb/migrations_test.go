package testdb

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// unused — номера, зарезервированные под задачу, которой миграция не
// понадобилась. Занимать их нельзя: база выше номера его уже не получит.
var unused = map[int]string{
	23: "S5-5 обошлась без миграции (ADR-038)",
}

// TestMigrationsAreContiguous: golang-migrate накатывает только версии выше
// текущей, поэтому база, успевшая дойти до N+1 без N, миграцию N уже никогда
// не получит («no change»). Номера идут подряд с 0001 (кроме unused), у
// каждого up — свой down.
func TestMigrationsAreContiguous(t *testing.T) {
	entries, err := os.ReadDir(migrations)
	if err != nil {
		t.Fatal(err)
	}
	ups, downs := map[int]string{}, map[int]bool{}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".up.sql") && !strings.HasSuffix(name, ".down.sql") {
			continue
		}
		num, _, ok := strings.Cut(name, "_")
		v, err := strconv.Atoi(num)
		if !ok || err != nil || len(num) != 4 {
			t.Fatalf("migration %s: want NNNN_name.{up,down}.sql", name)
		}
		if strings.HasSuffix(name, ".down.sql") {
			downs[v] = true
			continue
		}
		if prev, dup := ups[v]; dup {
			t.Fatalf("migration version %d used twice: %s and %s", v, prev, name)
		}
		ups[v] = name
	}
	versions := make([]int, 0, len(ups))
	for v := range ups {
		versions = append(versions, v)
	}
	if len(versions) == 0 {
		t.Fatalf("no migrations in %s", filepath.Clean(migrations))
	}
	sort.Ints(versions)
	for v, why := range unused {
		if name, ok := ups[v]; ok {
			t.Errorf("migration %s takes number %04d reserved as unused (%s)", name, v, why)
		}
	}
	want := 1
	for _, v := range versions {
		for unused[want] != "" && want != v {
			want++
		}
		if v != want {
			t.Fatalf("migration %04d missing before %s", want, ups[v])
		}
		want++
		if !downs[v] {
			t.Errorf("migration %s has no down", ups[v])
		}
	}
	for v := range downs {
		if _, ok := ups[v]; !ok {
			t.Errorf("down migration %04d has no up", v)
		}
	}
}
