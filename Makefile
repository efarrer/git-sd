.PHONY: build test clean

build:
	go build -o git-sd ./cmd/git-sd

test:
	go test -v -count=1 -parallel 8 -timeout 90m ./integration/

clean:
	rm -f git-sd
