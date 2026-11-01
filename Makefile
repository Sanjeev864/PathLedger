VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/sanjeev864/pathledger/internal/buildinfo.Version=$(VERSION)
# Agent targets: x86 servers, Raspberry Pi (arm64/armv7) and MIPS OpenWrt routers.
AGENT_PLATFORMS := linux/amd64 linux/arm64 linux/arm/7 linux/mipsle/softfloat linux/mips/softfloat

.PHONY: build test test-db lint e2e dist clean

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

dist:
	@mkdir -p dist
	@for p in $(AGENT_PLATFORMS); do \
		os=$$(echo $$p | cut -d/ -f1); arch=$$(echo $$p | cut -d/ -f2); extra=$$(echo $$p | cut -d/ -f3); \
		name=pathledger-agent-$$os-$$arch$${extra:+-$$extra}; \
		env CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch GOARM=$$( [ $$arch = arm ] && echo $$extra ) \
		    GOMIPS=$$( case $$arch in mips*) echo $$extra;; esac ) \
		    go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$$name ./cmd/pathledger-agent && echo "built dist/$$name"; \
	done

clean:
	rm -rf bin dist
