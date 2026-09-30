# -------------------------------------------------------------------------------
# Test Configuration Server
#
# Author: Alex Freidah
#
# cinc-server-ng built from the module this repository already depends on,
# rather than pulled: the project's published images are not anonymously
# pullable, and pinning the build to the same version the tests link against
# keeps one answer to "which server is this".
#
# It is a single static binary with no Ruby and no database, so the result is a
# scratch image that starts in about a hundred milliseconds.
# -------------------------------------------------------------------------------

ARG CINC_SERVER_VERSION=v0.14.0

FROM golang:1.27-alpine AS build
ARG CINC_SERVER_VERSION
RUN apk add --no-cache git
RUN GOBIN=/out go install \
      github.com/cinc-project/cinc-server-ng/cmd/cinc-server-ng@${CINC_SERVER_VERSION}

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=build /out/cinc-server-ng /usr/local/bin/cinc-server-ng
ENTRYPOINT ["/usr/local/bin/cinc-server-ng"]
