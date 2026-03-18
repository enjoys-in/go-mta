# Build stage
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /gomta ./cmd/gomta

# Build a tiny healthcheck binary
RUN printf 'package main\\nimport(\"net/http\";\"os\")\\nfunc main(){r,e:=http.Get(\"http://localhost:7140/api/v1/health\");if e!=nil||r.StatusCode!=200{os.Exit(1)}}' > /tmp/hc.go && \
    CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /healthcheck /tmp/hc.go

# Runtime stage
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /gomta /usr/local/bin/gomta
COPY --from=builder /healthcheck /usr/local/bin/healthcheck
COPY configs/ /etc/gomta/

EXPOSE 7140

ENTRYPOINT ["gomta"]
CMD ["--config-dir", "/etc/gomta"]
