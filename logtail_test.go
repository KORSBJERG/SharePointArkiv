package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogTailFollowsAppendAndReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), archiveLogName)
	tail := logTail{path: path}
	write := func(data string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	read := func(want string) {
		t.Helper()
		got, changed, err := tail.read()
		if err != nil || !changed || got != want {
			t.Fatalf("tail = %q, changed %t, err %v; want %q", got, changed, err, want)
		}
	}
	write("Kopierer æ.csv\nKopieret")
	read("Kopierer æ.csv\n")
	if _, changed, err := tail.read(); changed || err != nil {
		t.Fatalf("unchanged file: changed=%t, err=%v", changed, err)
	}
	out, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := out.WriteString(" til SharePoint\n"); err != nil {
		out.Close()
		t.Fatal(err)
	}
	read("Kopierer æ.csv\nKopieret til SharePoint\n")
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	write("Kort log efter nulstilling\n")
	read("Kort log efter nulstilling\n")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	replacement := strings.Repeat("n", int(before.Size())-1) + "\n"
	write(replacement)
	if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	read(replacement)
	write(strings.Repeat("ærøskøbing\n", logViewBytes) + "Seneste linje\nGemt delvis")
	got, changed, err := tail.read()
	if err != nil || !changed || len(got) > logViewBytes ||
		!strings.HasPrefix(got, "ærøskøbing\n") || !strings.HasSuffix(got, "Seneste linje\n") {
		t.Fatalf("bounded tail: length=%d, changed=%t, err=%v", len(got), changed, err)
	}
}
