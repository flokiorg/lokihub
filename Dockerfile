# Pinned by digest (node:20-alpine) for supply-chain reproducibility - a tag
# can be repointed to different content later, a digest can't. Re-resolve with
# `docker pull node:20-alpine && docker inspect --format='{{index .RepoDigests 0}}' node:20-alpine`
# when intentionally bumping.
FROM node@sha256:fb4cd12c85ee03686f6af5362a0b0d56d50c58a04632e6c0fb8363f609372293 AS frontend

# Set the base path for the frontend build
# This can be overridden at build time with --build-arg BASE_PATH=<url> e.g. --build-arg BASE_PATH=/hub
# Allows to build a frontend that can be served from a subpath, e.g. /hub
ARG BASE_PATH
WORKDIR /build
COPY frontend ./frontend
RUN echo "Building frontend with base path $BASE_PATH"
RUN cd frontend && yarn install --network-timeout 3000000 && yarn build:http

# Pinned by digest (golang:1.26) - see frontend stage's comment above for why.
# Was golang:1.24, which no longer builds this repo at all: go.mod requires
# go >= 1.26.1 and GOTOOLCHAIN=local in this image can't satisfy that.
FROM golang@sha256:ae5a2316d12f3e78fd99177dad452e6ad4f240af2d71d57b480c3477f250fec6 AS builder

ARG TARGETPLATFORM
ARG BUILDPLATFORM
ARG TAG

RUN echo "I am running on $BUILDPLATFORM, building for $TARGETPLATFORM, release tag $TAG"

RUN apt-get update && \
   apt-get install -y gcc

ENV CGO_ENABLED=1
ENV GOOS=linux
#ENV GOARCH=$GOARCH

#RUN echo "AAA $GOARCH"

# Move to working directory /build
WORKDIR /build

# Copy and download dependency using go mod
COPY go.mod .
COPY go.sum .


RUN GOARCH=$(echo "$TARGETPLATFORM" | cut -d'/' -f2) go mod download

# Copy the code into the container
COPY . .

COPY --from=frontend /build/frontend/dist ./frontend/dist

RUN GOARCH=$(echo "$TARGETPLATFORM" | cut -d'/' -f2) go build \
   -ldflags="-X 'github.com/flokiorg/lokihub/version.Tag=$TAG'" \
   -o main cmd/http/main.go

RUN GOARCH=$(echo "$TARGETPLATFORM" | cut -d'/' -f2) go build \
   -o db_migrate cmd/db_migrate/main.go

# Start a new, final image to reduce size.
# Pinned by digest (debian:12-slim) - see frontend stage's comment above for why.
FROM debian@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818 AS final

#
# # Copy the binaries and entrypoint from the builder image.
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /build/main /bin/
COPY --from=builder /build/db_migrate /bin/

ENTRYPOINT [ "/bin/main" ]
