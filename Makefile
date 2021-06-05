all:
	@echo "try: make test"

test: lint
	go test -race -covermode=atomic ./...
	# Test 32 bit OSes.
	GOOS=linux GOARCH=386 go build .
	GOOS=freebsd GOARCH=386 go build .

lint:
	golangci-lint --version
	GOOS=linux golangci-lint run --enable-all -D nlreturn,exhaustivestruct,interfacer,golint,scopelint,maligned
	GOOS=darwin golangci-lint run --enable-all -D nlreturn,exhaustivestruct,interfacer,golint,scopelint,maligned
	GOOS=windows golangci-lint run --enable-all -D nlreturn,exhaustivestruct,interfacer,golint,scopelint,maligned
	GOOS=freebsd golangci-lint run --enable-all -D nlreturn,exhaustivestruct,interfacer,golint,scopelint,maligned
