FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder

WORKDIR /workspace

COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o manager .

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532

LABEL org.opencontainers.image.source="https://github.com/jvillalbaj2lc/k8-hot-shrunk-requests"
LABEL org.opencontainers.image.title="CPU Request Shrink Controller"
LABEL org.opencontainers.image.description="Kubernetes controller for in-place CPU request shrinking"
LABEL org.opencontainers.image.licenses="MIT"

ENTRYPOINT ["/manager"]
