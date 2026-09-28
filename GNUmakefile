default: build

.PHONY: build
build:
	go build -v ./...

.PHONY: test
test:
	go test -v -count=1 -timeout 120s ./...
	python3 examples/data-sources/cloudlab_all_availability/test_cloudlab_nodetypes.py

.PHONY: test-race
test-race:
	go test -race -count=1 -timeout 120s ./...

.PHONY: lint
lint:
	golangci-lint run ./...

.PHONY: fmt
fmt:
	gofmt -w -s .

.PHONY: tidy
tidy:
	go mod tidy

.PHONY: generate
generate:
	go generate ./...

.PHONY: api-diff
api-diff: ## Compare checked-in OpenAPI spec with upstream. Usage: make api-diff
	@echo "Fetching upstream spec from GitLab..."
	@curl -fsSL "https://gitlab.flux.utah.edu/emulab/portal-api/-/raw/master/openapi.json" -o /tmp/cloudlab-upstream.json
	@jq --sort-keys . api/openapi.json > /tmp/cloudlab-local.json
	@jq --sort-keys . /tmp/cloudlab-upstream.json > /tmp/cloudlab-remote.json
	@if diff -q /tmp/cloudlab-local.json /tmp/cloudlab-remote.json > /dev/null 2>&1; then \
		echo "No changes detected — spec is up to date."; \
	else \
		echo "Changes detected between local spec and upstream:"; \
		diff /tmp/cloudlab-local.json /tmp/cloudlab-remote.json || true; \
		echo ""; \
		echo "Run 'make api-update' to pull the latest spec."; \
	fi

.PHONY: api-update
api-update: ## Pull the latest OpenAPI spec from upstream GitLab
	@echo "Updating api/openapi.json from upstream..."
	@curl -fsSL "https://gitlab.flux.utah.edu/emulab/portal-api/-/raw/master/openapi.json" -o api/openapi.json
	@echo "Done. Review changes with: git diff api/openapi.json"

.PHONY: clean
clean:
	rm -f terraform-provider-cloudlab

.PHONY: install
install: build
	mkdir -p ~/.terraform.d/plugins/registry.terraform.io/srmanda-cs/cloudlab/0.1.0/linux_amd64
	cp terraform-provider-cloudlab ~/.terraform.d/plugins/registry.terraform.io/srmanda-cs/cloudlab/0.1.0/linux_amd64/
