run:
	@go run *.go

test:
	@gotest -race -v ./...
