from golang:alpine as builder

workdir /go/src

copy go.mod go.sum ./
run go mod download

copy . .
run go build -v ./

from alpine
copy --from=builder /go/src/plakar-edge /bin/plakar-edge
entrypoint ["/bin/plakar-edge"]
