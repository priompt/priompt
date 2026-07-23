.PHONY: gen build test

gen:        ## regenerate Go stubs in the sibling proto repo (needs `buf`)
	cd ../proto && buf generate

build:      ## build the server binary
	go build -o priompt ./cmd/priompt

test:
	go test ./...
