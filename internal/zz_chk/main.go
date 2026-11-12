package main

import (
	"bufio"
	"fmt"
	"os"

	"sqlp/dialect"
	"sqlp/internal/corpus"
)

func main() {
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		p := corpus.Parse(sc.Text(), dialect.BigQuery)
		if p.Err != nil {
			fmt.Printf("NO  %-75.75s %v\n", sc.Text(), p.Err)
		} else {
			fmt.Printf("ok  %.75s\n", sc.Text())
		}
	}
}
