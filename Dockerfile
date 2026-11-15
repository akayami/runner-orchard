FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
RUN apk add --no-cache ca-certificates
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd cmd
COPY internal internal
ARG TARGETOS=linux
ARG TARGETARCH=arm64
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOMAXPROCS=2 go build -p 2 -trimpath -ldflags='-s -w' -o /orchard ./cmd/orchard
FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /orchard /orchard
COPY LICENSE THIRD_PARTY_NOTICES.md /licenses/
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/orchard"]
CMD ["serve", "-config", "/config/server.json"]
