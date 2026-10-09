# Build
FROM golang:1.27-bookworm AS build
WORKDIR /src
# Development machines whose antivirus re-signs HTTPS can put that local root certificate in .certs/*.crt
# (git-ignored). It is trusted for the build and the running API. With no such file this changes nothing.
COPY go.mod .cert[s]/*.crt /usr/local/share/ca-certificates/
RUN rm -f /usr/local/share/ca-certificates/go.mod && update-ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go test ./... && go vet ./... && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

# Run
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/api /app/api
COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
ENV TZ=UTC
EXPOSE 18080
USER nonroot
ENTRYPOINT ["/app/api"]
