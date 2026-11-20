package index

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func openAt(t testing.TB, path string, opts Options) *Store {
	t.Helper()
	s, err := Open(path, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func openStore(t testing.TB) *Store {
	t.Helper()
	return openAt(t, filepath.Join(t.TempDir(), "index.db"), Options{})
}

func TestOpenCreatesPrivateFiles(t *testing.T) {
	old := umask(0o022)
	defer umask(old)
	db := filepath.Join(t.TempDir(), "index.db")
	s := openAt(t, db, Options{})
	if _, err := s.wdb.Exec(`INSERT INTO meta(key, value) VALUES('x', 1)`); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{db, db + "-wal", db + "-shm", db + ".lock"} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatalf("%s: %v", filepath.Base(p), err)
		}
		if m := fi.Mode().Perm(); m != 0o600 {
			t.Errorf("%s mode = %o, want 600", filepath.Base(p), m)
		}
	}
}

func TestOpenRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(dir, "index.db")
	if err := os.Symlink(target, db); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(db, Options{}); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("Open(symlink db) = %v, want ErrNotRegular", err)
	}
	// A symlinked lock file is refused too.
	db2 := filepath.Join(dir, "two.db")
	if err := os.Symlink(target, db2+".lock"); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(db2, Options{}); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("Open(symlink lock) = %v, want ErrNotRegular", err)
	}
	if fi, _ := os.Stat(target); fi.Mode().Perm() != 0o644 {
		t.Errorf("symlink target chmod-ed to %o", fi.Mode().Perm())
	}
}

func TestOpenIsExclusive(t *testing.T) {
	db := filepath.Join(t.TempDir(), "index.db")
	s, err := Open(db, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(db, Options{}); !errors.Is(err, ErrLocked) {
		t.Fatalf("second Open = %v, want ErrLocked", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(db, Options{})
	if err != nil {
		t.Fatalf("Open after Close: %v", err)
	}
	s2.Close()
}

func TestOpenRebuildsCorruptDatabase(t *testing.T) {
	for name, data := range map[string]string{
		"garbage":   strings.Repeat("not a database\n", 400),
		"truncated": "SQLite format 3\x00" + strings.Repeat("\xff", 200),
	} {
		t.Run(name, func(t *testing.T) {
			db := filepath.Join(t.TempDir(), "index.db")
			if err := os.WriteFile(db, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			s := openAt(t, db, Options{})
			srcs, err := s.Sources()
			if err != nil || len(srcs) != 0 {
				t.Fatalf("Sources() = %v, %v", srcs, err)
			}
		})
	}
}

func TestSchemaVersionMismatchRebuilds(t *testing.T) {
	db := filepath.Join(t.TempDir(), "index.db")
	s, err := Open(db, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.wdb.Exec(`INSERT INTO sources(path, kind) VALUES('/x', 'mbox')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.wdb.Exec(`PRAGMA user_version = 999`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s = openAt(t, db, Options{})
	srcs, err := s.Sources()
	if err != nil || len(srcs) != 0 {
		t.Fatalf("after version change Sources() = %v, %v", srcs, err)
	}
}

func TestFTS5Available(t *testing.T) {
	s := openStore(t)
	var n int
	if err := s.rdb.QueryRow(`SELECT count(*) FROM messages_fts WHERE messages_fts MATCH 'x'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
}

func TestCachePath(t *testing.T) {
	dir := t.TempDir()
	a, err := CachePath(dir, "/data/mail/a")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := CachePath(dir, "/data/mail/b")
	a2, _ := CachePath(dir, "/data/mail/a/")
	if filepath.Dir(a) != dir || !strings.HasSuffix(a, ".db") {
		t.Errorf("CachePath = %q", a)
	}
	if a == b || a != a2 {
		t.Errorf("a=%q b=%q a2=%q", a, b, a2)
	}
	if strings.Contains(a, "mail") {
		t.Errorf("CachePath leaks the input path: %q", a)
	}
}

func TestResetForgetsEverything(t *testing.T) {
	s := openStore(t)
	a := newFake(t, "mbox", "a", rawEntry("A", 1, "one"))
	index(t, s, a)
	if err := s.Reset(); err != nil {
		t.Fatal(err)
	}
	if srcs, _ := s.Sources(); len(srcs) != 0 {
		t.Fatalf("sources after Reset = %+v", srcs)
	}
	index(t, s, a)
	if a.scans.Load() != 2 {
		t.Errorf("scans = %d, want a full re-scan after Reset", a.scans.Load())
	}
	if r := search(t, s, SearchParams{Q: "one"}); r.Total != 1 {
		t.Errorf("total = %d", r.Total)
	}
}

func TestOpenRefusesSymlinkedWALAndSHM(t *testing.T) {
	for _, suffix := range []string{"-wal", "-shm"} {
		dir := t.TempDir()
		target := filepath.Join(dir, "target")
		if err := os.WriteFile(target, []byte("keep"), 0o644); err != nil {
			t.Fatal(err)
		}
		db := filepath.Join(dir, "index.db")
		if err := os.Symlink(target, db+suffix); err != nil {
			t.Fatal(err)
		}
		if s, err := Open(db, Options{}); !errors.Is(err, ErrNotRegular) {
			if s != nil {
				s.Close()
			}
			t.Fatalf("%s symlink: Open = %v, want ErrNotRegular", suffix, err)
		}
		if b, _ := os.ReadFile(target); string(b) != "keep" {
			t.Errorf("%s symlink target written: %q", suffix, b)
		}
		if fi, _ := os.Stat(target); fi.Mode().Perm() != 0o644 {
			t.Errorf("%s symlink target chmod-ed", suffix)
		}
	}
}

func TestResetKeepsIDsUnique(t *testing.T) {
	s := openStore(t)
	a := newFake(t, "mbox", "a", rawEntry("A", 1, "one"))
	index(t, s, a)
	old := allRows(t, s)[0].ID
	if err := s.Reset(); err != nil {
		t.Fatal(err)
	}
	index(t, s, a)
	if id := allRows(t, s)[0].ID; id <= old {
		t.Errorf("id after Reset = %d, want > %d", id, old)
	}
}

func TestResetLeavesNoMailText(t *testing.T) {
	db := filepath.Join(t.TempDir(), "index.db")
	s := openAt(t, db, Options{})
	const marker = "Zqxjvmarkerword"
	index(t, s, newFake(t, "mbox", "a", rawEntry("A", 1, marker)))
	if err := s.Reset(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{db, db + "-wal"} {
		b, _ := os.ReadFile(p)
		if strings.Contains(string(b), marker) {
			t.Errorf("%s still holds indexed text after Reset", filepath.Base(p))
		}
	}
}

func TestOpenCreatesPrivateCacheDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache", "mail-export-viewer")
	db, err := CachePath(dir, "/data/in")
	if err != nil {
		t.Fatal(err)
	}
	openAt(t, db, Options{})
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m := fi.Mode().Perm(); m != 0o700 {
		t.Errorf("cache dir mode = %o, want 700", m)
	}
}

// SQLite's temporary files (sorts, FTS merges, VACUUM) hold index data:
// they go to the private cache directory, never to TMPDIR (issue #16).
func TestTempFilesInCacheDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "it's cache")
	s, err := Open(filepath.Join(dir, "index.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for name, db := range map[string]*sql.DB{"writer": s.wdb, "reader": s.rdb} {
		var got string
		if err := db.QueryRow(`PRAGMA temp_store_directory`).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != dir {
			t.Errorf("%s temp_store_directory = %q, want %q", name, got, dir)
		}
	}
}
