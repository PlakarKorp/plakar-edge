from golang:alpine as builder

workdir /go/src

copy go.mod go.sum ./
run go mod download

copy . .
run go build -v ./

from alpine
# ssh, ssh-add and ssh-agent for the sftp integration
run apk add --no-cache openssh-client
copy --from=builder /go/src/plakar-edge /bin/plakar-edge
entrypoint ["/bin/plakar-edge"]
