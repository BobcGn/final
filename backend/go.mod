module github.com/BobcGn/final/backend

go 1.26.1

require (
	github.com/gorilla/websocket v1.5.3
	github.com/jackc/pgx/v5 v5.9.2
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/rogpeppe/go-internal v1.16.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/text v0.40.0 // indirect
)

// pgx and puddle only pull testify for their own test suites, which this module
// never builds. Pinning it keeps the module graph resolvable from the local
// module cache in offline environments; no production dependency is affected.
replace github.com/stretchr/testify => github.com/stretchr/testify v1.9.0
