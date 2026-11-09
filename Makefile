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
# run serves the site at http://localhost:8080. With the 1Password CLI signed
# in, it also syncs the Zotero library into ./data and serves /library/.
run:
	ZOTERO_USER_ID=$$(op read op://Personal/Zotero/user-id 2>/dev/null) \
	ZOTERO_API_KEY=$$(op read op://Personal/Zotero/api-key 2>/dev/null) \
	LIBRARY_DIR=./data LIBRARY_DEV_OWNER=1 WEBSITE_ADDR=localhost:8080 \
	go run .
# up starts the image just built, then removes all but the two newest
# versions of it, so a deploy can still be rolled back by one.
up:
	docker compose up -d
	-docker images $(NAME) --format '{{.Tag}}' | grep -vx latest | tail -n +3 | sed 's/^/$(NAME):/' | xargs -r docker rmi
	-docker image prune -f
down:
	docker compose down
clean: down
	rm -rf $(NAME)
	docker image rm -f $(shell docker image ls -f "dangling=true" -q) 2> /dev/null; true
	docker image rm -f $(NAME):latest $(NAME):$(VERSION) 2> /dev/null; true
