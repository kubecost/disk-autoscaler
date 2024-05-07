FROM golang:latest as build-env

RUN mkdir /app
WORKDIR /app

# This ensures that CGO is disabled for go test running AND for the build
# step. This prevents a build failure when building an ARM64 image with
# docker buildx. I believe this is because the ARM64 version of the
# golang:latest image does not contain GCC, while the AMD64 version does.
ARG CGO_ENABLED=0

# Copy and download code and dependencies
COPY ./go.mod ./go.mod
COPY ./go.sum ./go.sum
# Get dependencies - will be cached if we won't change mod/sum
RUN go mod download
# COPY the source code as the last step
COPY ./cmd ./cmd
COPY ./pkg ./pkg

RUN go test ./...
# Build the binary
RUN cd ./cmd/diskautoscaler && set -e ;\
    go build -a -installsuffix cgo -o /go/bin/app

FROM alpine:latest
RUN apk add --update --no-cache ca-certificates
COPY --from=build-env /go/bin/app /go/bin/app

EXPOSE 9730

ENTRYPOINT ["/go/bin/app"]