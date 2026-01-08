# -----------------------------------------------------------------------------
# Linux AMD64 Builder (Modern - Debian 12 / Ubuntu 24.04+)
# -----------------------------------------------------------------------------
FROM debian:bookworm

ARG GO_VERSION=1.24.9

# Install Go manually
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    wget \
    git \
    && rm -rf /var/lib/apt/lists/*

RUN wget -q https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz && \
    tar -C /usr/local -xzf go${GO_VERSION}.linux-amd64.tar.gz && \
    rm go${GO_VERSION}.linux-amd64.tar.gz

ENV PATH="/usr/local/go/bin:${PATH}"
ENV GOPATH=/go
ENV PATH=$GOPATH/bin:$PATH

RUN mkdir -p "$GOPATH/src" "$GOPATH/bin" && chmod -R 777 "$GOPATH"

# Install Dependencies (Modern: WebKit 4.1)
RUN apt-get update && apt-get install -y \
    build-essential \
    libgtk-3-dev \
    libwebkit2gtk-4.1-dev \
    nsis \
    unzip \
    zip \
    p7zip-full \
    pkg-config \
    file \
    desktop-file-utils \
    && rm -rf /var/lib/apt/lists/*

# Install Node.js 20
RUN curl -fsSL https://deb.nodesource.com/setup_20.x | bash - && \
    apt-get install -y nodejs && \
    npm install -g yarn

# Install Wails
RUN go install github.com/wailsapp/wails/v2/cmd/wails@v2.11.0

# Install nfpm (for DEB/RPM packaging)
RUN go install github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.41.3

# Install linuxdeploy (x86_64)
RUN mkdir -p /usr/local/bin/tools && \
    wget -q -O /usr/local/bin/tools/linuxdeploy-x86_64.AppImage https://github.com/linuxdeploy/linuxdeploy/releases/download/continuous/linuxdeploy-x86_64.AppImage && \
    chmod +x /usr/local/bin/tools/linuxdeploy-x86_64.AppImage

ENV PATH="/usr/local/bin/tools:${PATH}"
ENV LINUXDEPLOY_BIN="/usr/local/bin/tools/linuxdeploy-x86_64.AppImage"

# Build Environment Variables
ENV GOOS=linux
ENV GOARCH=amd64
ENV CC=gcc
ENV CGO_ENABLED=1

WORKDIR /build
