install:
	rm -f hop
	go build -o hop ./cmd/hop
	install -C ./hop /usr/local/bin/hop
