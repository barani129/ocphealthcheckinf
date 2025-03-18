# Build the manager binary
FROM golang:1.23 AS builder
ARG TARGETOS
ARG TARGETARCH

# Create non-root user
RUN groupadd golanguser
RUN useradd -ms /bin/bash golanguser -g golanguser
USER golanguser
WORKDIR /home/golanguser

# Copy the Go Modules manifests
COPY --chown=golanguser:golanguser go.mod go.mod
COPY --chown=golanguser:golanguser go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY --chown=golanguser:golanguser cmd/main.go cmd/main.go
COPY --chown=golanguser:golanguser api/ api/
COPY --chown=golanguser:golanguser internal/controller/ internal/controller/
COPY --chown=golanguser:golanguser internal/util/ internal/util/

# Build
# the GOARCH has not a default value to allow the binary be built according to the host where the command
# was called. For example, if we call make docker-build in a local env which has the Apple Silicon M1 SO
# the docker BUILDPLATFORM arg will be linux/arm64 when for Apple x86 it will be linux/amd64. Therefore,
# by leaving it empty we can ensure that the container and binary shipped on it will have the same platform.
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o manager cmd/main.go

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM alpine:latest
RUN apk add --no-cache bash
RUN apk add --no-cache mailx

# Create non-root user 
RUN addgroup -g 65532 golanguser
RUN addgroup -S golanggroup && adduser -S golanguser -u 65532 -G golanguser
RUN chmod 777 /home/golanguser
WORKDIR /home/golanguser
COPY --from=builder /home/golanguser/manager .
RUN mkdir /home/golanguser/files && chown golanguser:golanguser /home/golanguser/files
USER 65532:65532

ENTRYPOINT ["/home/golanguser/manager"]
