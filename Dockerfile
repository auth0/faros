# Build the manager binary
FROM golang:1.10.3 as builder

# Install Dep
RUN curl https://raw.githubusercontent.com/golang/dep/master/install.sh | sh

# Copy in the go src
WORKDIR /go/src/github.com/pusher/faros
COPY Gopkg.lock Gopkg.lock
COPY Gopkg.toml Gopkg.toml
# Fetch dependencies - doing this here allows docker to cache unless Gopkg changes
RUN dep ensure --vendor-only
COPY pkg/    pkg/
COPY cmd/    cmd/

# Build
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -o manager github.com/pusher/faros/cmd/manager

# Copy the controller-manager into a thin image
FROM alpine:3.8
RUN apk --no-cache add ca-certificates
WORKDIR /bin
COPY --from=builder /go/src/github.com/pusher/faros/manager .
ENTRYPOINT ["/bin/manager"]
