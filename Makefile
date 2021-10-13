all: install

GO_BUILDER_VERSION=v1.16.3

ldflags=-X=github.com/myelnet/pop/infra/build.Version=$(shell cat ./infra/VERSION.txt)-$(shell git describe --always --match=NeVeRmAtCh --dirty 2>/dev/null || git rev-parse --short HEAD 2>/dev/null)

install:
	rm -f pop
	go build -ldflags=$(ldflags) -o pop ./cmd/pop
	install -C ./pop /usr/local/bin/pop

# builds image locally
snapshot:
	docker build -f infra/releaser/Dockerfile -t myel/pop-golang-cross .
	docker run --rm --privileged \
                -v $(CURDIR):/pop \
                -v /var/run/docker.sock:/var/run/docker.sock \
                -w /pop \
                myel/pop-golang-cross --snapshot --rm-dist

# uses latest docker-hub image
snapshot-light:
	docker run --rm --privileged \
                -v $(CURDIR):/pop \
                -v /var/run/docker.sock:/var/run/docker.sock \
                -w /pop \
                docker.io/myel/pop-golang-cross:latest --snapshot --rm-dist
