all: install

GO_BUILDER_VERSION=v1.16.3

ldflags=-X=github.com/myelnet/pop/build.Version=$(shell cat ./build/VERSION.txt)-$(shell git describe --always --match=NeVeRmAtCh --dirty 2>/dev/null || git rev-parse --short HEAD 2>/dev/null)

.update-modules:
	git submodule update --init --recursive
	@touch $@

install:
	rm -f pop
	go build -ldflags=$(ldflags) -o pop ./cmd/pop
	install -C ./pop /usr/local/bin/pop
