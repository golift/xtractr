all:
	@echo "try: make test"

test: lint
	go test -race -covermode=atomic ./...
	# Test 32 bit OSes.
	GOOS=linux GOARCH=386 go build .
	GOOS=windows GOARCH=386 go build .
	GOOS=freebsd GOARCH=386 go build .

lint:
	golangci-lint --version
	GOOS=linux golangci-lint run
	GOOS=darwin golangci-lint run
	GOOS=windows golangci-lint run
	GOOS=freebsd golangci-lint run
