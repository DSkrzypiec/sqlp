module sqlp/tools/corpusgen

go 1.26.5

require (
	github.com/pganalyze/pg_query_go/v5 v5.1.0
	sqlp v0.0.0
)

require google.golang.org/protobuf v1.31.0 // indirect

replace sqlp => ../..
