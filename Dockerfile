# Copyright 2020 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

FROM alpine
# Publishing calls the model gateway and the GitHub API, and the login
# fetches latere's keys, all over TLS.
RUN apk add --no-cache ca-certificates
COPY website /app/website
EXPOSE 80
CMD ["/app/website"]
