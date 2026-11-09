.PHONY: fmt test vet check-contracts check

fmt:
	go fmt ./...

test:
	go test ./...

vet:
	go vet ./...

check-contracts:
	go run ./cmd/openfox-server migrate --dir db/migrations --check

check: fmt test vet check-contracts
