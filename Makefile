all:
	@echo "try: make test"

test: lint
	go test -race -covermode=atomic ./...
	# Test 32 bit OSes.
	GOOS=linux GOARCH=386 go build .
	GOOS=freebsd GOARCH=386 go build .

lint:
	GOOS=linux golangci-lint run --enable-all -D nlreturn,exhaustivestruct
	GOOS=darwin golangci-lint run --enable-all -D nlreturn,exhaustivestruct
	GOOS=windows golangci-lint run --enable-all -D nlreturn,exhaustivestruct
	GOOS=freebsd golangci-lint run --enable-all -D nlreturn,exhaustivestruct
