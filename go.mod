module github.com/kangheeyong/drp

go 1.25.0

require (
	github.com/golang/snappy v1.0.0
	github.com/hashicorp/yamux v0.1.2
	golang.org/x/crypto v0.49.0
	golang.org/x/net v0.52.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/kr/pretty v0.3.1 // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	golang.org/x/text v0.35.0 // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
)

replace github.com/hashicorp/yamux => github.com/fatedier/yamux v0.0.0-20250825093530-d0154be01cd6
