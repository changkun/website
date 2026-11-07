# Copyright 2020 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

NAME=website
VERSION = $(shell git describe --always --tags)
all:
	go build -o $(NAME)
test:
	go test -race -cover ./...
build: test
	CGO_ENABLED=0 GOOS=linux go build -o $(NAME)
	docker buildx build -t $(NAME):$(VERSION) -t $(NAME):latest --load .
# library serves the site locally with the Zotero library synced into ./data,
# at http://localhost:8080/library/. It needs the 1Password CLI, signed in.
library:
	ZOTERO_USER_ID=$$(op read op://Personal/Zotero/user-id) \
	ZOTERO_API_KEY=$$(op read op://Personal/Zotero/api-key) \
	LIBRARY_DIR=./data LIBRARY_DEV_OWNER=1 WEBSITE_ADDR=localhost:8080 \
	go run .
up:
	docker compose up -d
down:
	docker compose down
clean: down
	rm -rf $(NAME)
	docker image rm -f $(shell docker image ls -f "dangling=true" -q) 2> /dev/null; true
	docker image rm -f $(NAME):latest $(NAME):$(VERSION) 2> /dev/null; true
