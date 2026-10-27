package testdb

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestMigrationsAreContiguous: golang-migrate накатывает только версии выше
// текущей, поэтому база, успевшая дойти до N+1 без N, миграцию N уже никогда
// не получит («no change»). Номера идут подряд с 0001, у каждого up — свой down.
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
	for i, v := range versions {
		if v != i+1 {
			t.Fatalf("migration %04d missing before %s", i+1, ups[v])
		}
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
