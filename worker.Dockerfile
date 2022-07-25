# Build the manager binary
FROM golang:1.17 as builder

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY cmd/worker/main.go cmd/worker/main.go
COPY api/ api/
COPY controllers/ controllers/

# Build
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -o worker cmd/worker/main.go

FROM alpine:latest

RUN apk --no-cache add curl

RUN apk add --update docker openrc

RUN rc-update add docker boot

RUN cd /usr/local/bin && \
    curl https://releases.hashicorp.com/terraform/1.2.3/terraform_1.2.3_linux_amd64.zip -o terraform.zip && \
    unzip terraform.zip && \
    rm terraform.zip

COPY --from=builder /workspace/worker /usr/local/bin/

WORKDIR /opt/manifests

CMD ["/usr/local/bin/worker"]