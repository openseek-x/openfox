GO_PACKAGES := ./cmd/... ./internal/... ./tests/...

.PHONY: fmt test test-integration vet check-contracts check

fmt:
	go fmt $(GO_PACKAGES)

test:
	go test $(GO_PACKAGES)

test-integration:
	python3 tests/integration/run_all.py

vet:
	go vet $(GO_PACKAGES)

check-contracts:
	go run ./cmd/openfox-server migrate --dir db/migrations --check

check: fmt test vet check-contracts test-integration
