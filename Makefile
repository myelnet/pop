all: install

FFI_PATH:=./extern/filecoin-ffi/
FFI_DEPS:=.install-filcrypto
FFI_DEPS:=$(addprefix $(FFI_PATH),$(FFI_DEPS))
GO_BUILDER_VERSION=v1.16.3

$(FFI_DEPS): .filecoin-build ;

.filecoin-build: $(FFI_PATH)
	$(MAKE) -C $(FFI_PATH) $(FFI_DEPS:$(FFI_PATH)%=%)
	@touch $@

.update-modules:
	git submodule update --init --recursive
	@touch $@

build: .update-modules .filecoin-build

install:
	rm -f pop
	go build -o pop ./cmd/pop
	install -C ./pop /usr/local/bin/pop

snapshot:
	docker build -f build/Dockerfile -t pop/golang-cross .
	docker run --rm --privileged \
		-v $(CURDIR):/pop \
		-v /var/run/docker.sock:/var/run/docker.sock \
		-v $(HOME)/go/src:/go/src \
		-w /pop \
		pop/golang-cross --snapshot --rm-dist
