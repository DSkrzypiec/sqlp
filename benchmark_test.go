package sqlp

import (
	"path/filepath"
	"testing"

	"sqlp/dialect"
	"sqlp/internal/corpus"
	"sqlp/internal/testkit"
	"sqlp/lexer"
)

// accepted returns the frozen statements the parser accepts, so timings come
// from real SQL rather than from error paths.
func accepted(b *testing.B, d *dialect.Dialect) (list []string, size int64) {
	b.Helper()
	files, _ := filepath.Glob(filepath.Join("testdata", "corpus", d.Name, "*.jsonl"))
	for _, f := range files {
		rows, err := corpus.Read[corpus.Record](f)
		if err != nil {
			b.Fatal(err)
		}
		for _, r := range rows {
			if !r.Reject && corpus.Parse(r.SQL, d).Err == nil {
				list = append(list, r.SQL)
				size += int64(len(r.SQL))
			}
		}
	}
	return list, size
}

// BenchmarkCorpus measures one pass over the frozen corpus per iteration:
//
//	go test -run '^$' -bench Corpus -benchmem . -dialect=postgres
func BenchmarkCorpus(b *testing.B) {
	for _, d := range dialect.All {
		if !testkit.Enabled(d.Name) {
			continue
		}
		list, size := accepted(b, d)
		b.Run(d.Name+"/lex", func(b *testing.B) {
			b.SetBytes(size)
			for b.Loop() {
				for _, sql := range list {
					if _, err := lexer.Tokenize(sql, d.Lexer); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
		b.Run(d.Name+"/parse", func(b *testing.B) {
			b.SetBytes(size)
			for b.Loop() {
				for _, sql := range list {
					if _, err := Parse(sql, d); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
		b.Run(d.Name+"/format", func(b *testing.B) {
			parsed := make([]corpus.Parsed, len(list))
			for i, sql := range list {
				parsed[i] = corpus.Parse(sql, d)
			}
			b.SetBytes(size)
			for b.Loop() {
				for _, p := range parsed {
					Format(p.Stmts, d)
				}
			}
		})
	}
}
