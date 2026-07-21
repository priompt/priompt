.PHONY: gen build test

gen:        ## generate Go stubs from proto (needs `buf`)
	buf generate

build: gen  ## build the server binary
	go build -o priompt ./cmd/priompt

test:
	go test ./...
