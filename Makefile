install:
	rm -f pop
	go build -o pop ./cmd/pop
	install -C ./pop /usr/local/bin/pop
