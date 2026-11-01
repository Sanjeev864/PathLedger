VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/sanjeev864/pathledger/internal/buildinfo.Version=$(VERSION)
# Agent targets: the computers a business already has (Windows and Mac PCs,
# Linux servers and cloud VMs). No special hardware is needed.
AGENT_PLATFORMS := linux/amd64 linux/arm64 windows/amd64 windows/arm64 darwin/amd64 darwin/arm64

.PHONY: build web test test-db lint e2e lab dist clean

# Builds the dashboard into internal/controller/webui/dist (embedded by go build).
web:
	cd web && npm ci && npm run check && npm run build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ ./cmd/...

test:
	go test -race ./...

# Requires PATHLEDGER_TEST_DATABASE_URL, e.g. postgres://postgres@127.0.0.1:5432/postgres?sslmode=disable
test-db:
	@test -n "$$PATHLEDGER_TEST_DATABASE_URL" || (echo "set PATHLEDGER_TEST_DATABASE_URL" && exit 1)
	go test -race -count=1 ./...

lint:
	@out=$$(gofmt -l .); test -z "$$out" || (echo "gofmt needed:"; echo "$$out"; exit 1)
	go vet ./...

# Requires PATHLEDGER_DATABASE_URL pointing at an empty database.
e2e: build
	./scripts/e2e.sh

# Fault-injection lab (Linux, root). Needs: pip install pyroute2, an empty
# database in PATHLEDGER_DATABASE_URL. Writes lab/results.md.
lab: build
	python3 -u lab/faultlab.py --bin ./bin --out lab/results.md

dist:
	@mkdir -p dist
	@for p in $(AGENT_PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		name=pathledger-agent-$$os-$$arch$$( [ $$os = windows ] && echo .exe ); \
		env CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
		    go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$$name ./cmd/pathledger-agent && echo "built dist/$$name" || exit 1; \
	done

clean:
	rm -rf bin dist web/node_modules
	find internal/controller/webui/dist -mindepth 1 ! -name .gitignore ! -name .keep -delete
