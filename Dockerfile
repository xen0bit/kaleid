# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/kaleid ./cmd/kaleid

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/kaleid /usr/local/bin/kaleid
EXPOSE 8000
ENV KALEID_LISTEN=:8000
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/kaleid"]
CMD ["serve"]
