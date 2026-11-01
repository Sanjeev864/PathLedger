# Builds either binary: docker build --build-arg BINARY=pathledger-controller ...
FROM golang:1.24-alpine AS build
ARG BINARY=pathledger-controller
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/sanjeev864/pathledger/internal/buildinfo.Version=${VERSION}" \
      -o /out/app ./cmd/${BINARY}

# Distroless static image: no shell, no package manager, non-root user.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /usr/local/bin/app
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/app"]
