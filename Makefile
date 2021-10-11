all: install

GO_BUILDER_VERSION=v1.16.3

ldflags=-X=github.com/myelnet/pop/infra/build.Version=$(shell cat ./infra/VERSION.txt)-$(shell git describe --always --match=NeVeRmAtCh --dirty 2>/dev/null || git rev-parse --short HEAD 2>/dev/null)

install:
	rm -f pop
	go build -ldflags=$(ldflags) -o pop ./cmd/pop
	install -C ./pop /usr/local/bin/pop

snapshot:
	docker build -f infra/releaser/Dockerfile -t pop/golang-cross .
	docker run --rm --privileged \
		-v $(CURDIR):/pop \
		-v /var/run/docker.sock:/var/run/docker.sock \
		-w /pop \
		pop/golang-cross --snapshot --rm-dist
	mv dist/pop-linux-arm64_linux_arm64/pop ./pop-arm64-linux
	mv dist/pop-darwin-arm64_darwin_arm64/pop ./pop-arm64-darwin
	mv dist/pop-linux-amd64_linux_arm64/pop ./pop-amd64-linux
	mv dist/pop-darwin-armd4_darwin_arm64/pop ./pop-amd64-darwin
