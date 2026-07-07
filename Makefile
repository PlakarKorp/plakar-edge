GO      = go
INSTDIR ?= /usr/local/bin

# plakar-edge is a single self-contained binary: it embeds the plaklet executor
# (pinned in go.mod) and runs it via the `plakar-edge plaklet` subcommand, so
# building the edge builds plaklet too — there is no separate binary to ship.
all: plakar-edge
.PHONY: all plakar-edge test install clean

plakar-edge:
	${GO} build -o plakar-edge .

test:
	${GO} test ./...

install: plakar-edge
	install -m 0755 plakar-edge ${INSTDIR}/plakar-edge

clean:
	rm -f plakar-edge
