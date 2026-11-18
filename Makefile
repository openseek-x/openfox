GO_PACKAGES := ./cmd/... ./internal/... ./tests/...

.PHONY: fmt test vet check-contracts check

fmt:
	go fmt $(GO_PACKAGES)

test:
	go test $(GO_PACKAGES)

vet:
	go vet $(GO_PACKAGES)

check-contracts:
	go run ./cmd/openfox-server migrate --dir db/migrations --check

check: fmt test vet check-contracts
