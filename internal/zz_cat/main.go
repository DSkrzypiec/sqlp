package main

import (
	"fmt"
	"regexp"
	"sort"

	"sqlp/dialect"
	"sqlp/internal/corpus"
)

func main() {
	known, _ := corpus.Read[corpus.Known]("testdata/known/bigquery.jsonl")
	at := regexp.MustCompile(`unexpected ("[^"]*"|end of input|[A-Za-z]+ [^,]*)`)
	count := map[string]int{}
	example := map[string]string{}
	for _, k := range known {
		if k.Kind != corpus.RejectsValid {
			continue
		}
		p := corpus.Parse(k.SQL, dialect.BigQuery)
		key := "?"
		if p.Err != nil {
			if m := at.FindString(p.Err.Error()); m != "" {
				key = m
			} else {
				key = p.Err.Error()
			}
		}
		if len(key) > 60 {
			key = key[:60]
		}
		count[key]++
		if _, ok := example[key]; !ok {
			example[key] = k.SQL
		}
	}
	keys := make([]string, 0, len(count))
	for k := range count {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return count[keys[i]] > count[keys[j]] })
	for _, k := range keys[:min(45, len(keys))] {
		ex := example[k]
		if len(ex) > 90 {
			ex = ex[:90]
		}
		fmt.Printf("%4d %-62s %q\n", count[k], k, ex)
	}
}
