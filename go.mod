module github.com/Xh12321/ctftools

go 1.22.0

require (
	github.com/google/uuid v1.6.0
	github.com/ncruces/go-sqlite3 v0.25.0
)

require (
	github.com/ncruces/julianday v1.0.0 // indirect
	github.com/tetratelabs/wazero v1.9.0 // indirect
	golang.org/x/sys v0.31.0 // indirect
)

replace github.com/google/uuid => ./vendor/github.com/google/uuid

replace github.com/ncruces/go-sqlite3 => ./vendor/github.com/ncruces/go-sqlite3

replace github.com/ncruces/julianday => ./vendor/github.com/ncruces/julianday

replace github.com/tetratelabs/wazero => ./vendor/github.com/tetratelabs/wazero

replace golang.org/x/sys => ./vendor/golang.org/x/sys

replace golang.org/x/crypto => ./vendor/golang.org/x/crypto
