.PHONY: build install clean run

BINARY_NAME=lazyhydra
INSTALL_PATH=$(HOME)/.local/bin

build:
	go build -o $(BINARY_NAME) .

install: build
	mkdir -p $(INSTALL_PATH)
	cp $(BINARY_NAME) $(INSTALL_PATH)/

clean:
	rm -f $(BINARY_NAME)

run: build
	./$(BINARY_NAME)
