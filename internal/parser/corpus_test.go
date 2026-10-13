package parser

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestCorpus checks the parser against real-world release names in
// testdata/corpus.tsv.
func TestCorpus(t *testing.T) {
	f, err := os.Open("testdata/corpus.tsv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	for line := 1; sc.Scan(); line++ {
		text := sc.Text()
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		c := strings.Split(text, "\t")
		if len(c) != 10 {
			t.Fatalf("line %d: %d columns, want 10", line, len(c))
		}
		n++
		m, ok := Parse(c[0])
		if c[1] == "none" {
			if ok {
				t.Errorf("line %d %q: parsed as %+v, want no result", line, c[0], m)
			}
			continue
		}
		want := Media{
			Kind: Kind(c[1]), Title: c[2], Year: num(t, c[3]), Season: num(t, c[4]),
			AirDate: undash(c[6]), Absolute: num(t, c[7]), Part: num(t, c[8]), Edition: undash(c[9]),
		}
		if c[5] != "-" {
			for _, e := range strings.Split(c[5], ",") {
				want.Episodes = append(want.Episodes, num(t, e))
			}
		}
		if !ok || !sameMedia(m, want) {
			t.Errorf("line %d %q:\n got  %+v (ok=%v)\n want %+v", line, c[0], m, ok, want)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if n < 100 {
		t.Errorf("corpus has %d names, want at least 100", n)
	}
}

func num(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatalf("bad number %q", s)
	}
	return n
}

func undash(s string) string {
	if s == "-" {
		return ""
	}
	return s
}

func sameMedia(a, b Media) bool {
	if len(a.Episodes) != len(b.Episodes) {
		return false
	}
	for i := range a.Episodes {
		if a.Episodes[i] != b.Episodes[i] {
			return false
		}
	}
	a.Episodes, b.Episodes = nil, nil
	return a.Kind == b.Kind && a.Title == b.Title && a.Year == b.Year && a.Season == b.Season &&
		a.AirDate == b.AirDate && a.Absolute == b.Absolute && a.Part == b.Part && a.Edition == b.Edition
}
