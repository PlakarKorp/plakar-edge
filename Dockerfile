from golang:bookworm as builder

workdir /go/src

copy go.mod go.sum ./
run go mod download

copy . .
run go build -v ./

# Connector packages are built against glibc.
from debian:bookworm-slim
# ssh, ssh-add and ssh-agent for the sftp integration
run apt-get update && apt-get install -y --no-install-recommends openssh-client ca-certificates && rm -rf /var/lib/apt/lists/*
copy --from=builder /go/src/plakar-edge /bin/plakar-edge
entrypoint ["/bin/plakar-edge"]
