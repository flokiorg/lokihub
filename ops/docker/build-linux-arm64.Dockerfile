# -----------------------------------------------------------------------------
# Stage 1: Generate ARM64 Sysroot
# -----------------------------------------------------------------------------
FROM debian:bullseye AS sysroot-gen

RUN dpkg --add-architecture arm64 && apt-get update && apt-get install -y \
    build-essential \
    crossbuild-essential-arm64 \
    libgtk-3-dev:arm64 \
    libwebkit2gtk-4.0-dev:arm64 \
    && rm -rf /var/lib/apt/lists/*

# -----------------------------------------------------------------------------
# Stage 2: Final Builder (ARM64 Cross-Compile)
# -----------------------------------------------------------------------------
FROM debian:bullseye

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

# Install Dependencies & Cross-Compilation Tools
RUN dpkg --add-architecture arm64 && \
    apt-get update && apt-get install -y \
    build-essential \
    crossbuild-essential-arm64 \
    libgtk-3-dev \
    libwebkit2gtk-4.0-dev \
    nsis \
    unzip \
    zip \
    p7zip-full \
    pkg-config \
    qemu-user-static \
    file \
    desktop-file-utils \
    squashfs-tools \
    && rm -rf /var/lib/apt/lists/*

# Copy ARM64 Sysroot
COPY --from=sysroot-gen /usr/lib/aarch64-linux-gnu /sysroot/arm64/usr/lib/aarch64-linux-gnu
COPY --from=sysroot-gen /usr/include /sysroot/arm64/usr/include
COPY --from=sysroot-gen /usr/share/pkgconfig /sysroot/arm64/usr/share/pkgconfig
COPY --from=sysroot-gen /lib/aarch64-linux-gnu /sysroot/arm64/lib/aarch64-linux-gnu
COPY --from=sysroot-gen /lib/ld-linux-aarch64.so.1 /sysroot/arm64/lib/ld-linux-aarch64.so.1

# Install Node.js 20
RUN curl -fsSL https://deb.nodesource.com/setup_20.x | bash - && \
    apt-get install -y nodejs && \
    npm install -g yarn

# Install Zig (for CGO cross-compilation reference if needed, though we use gcc-aarch64)
RUN cd /tmp && \
    wget -q https://ziglang.org/download/0.13.0/zig-linux-x86_64-0.13.0.tar.xz && \
    tar -xf zig-linux-x86_64-0.13.0.tar.xz && \
    mv zig-linux-x86_64-0.13.0 /opt/zig && \
    rm zig-linux-x86_64-0.13.0.tar.xz
ENV PATH="/opt/zig:${PATH}"

# Install Wails
RUN go install github.com/wailsapp/wails/v2/cmd/wails@v2.11.0

# Install nfpm (for DEB/RPM packaging)
RUN go install github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.41.3

# Install linuxdeploy (arm64) & Tools
RUN mkdir -p /usr/local/bin/tools && \
    wget -q -O /usr/local/bin/tools/linuxdeploy-aarch64.AppImage https://github.com/linuxdeploy/linuxdeploy/releases/download/continuous/linuxdeploy-aarch64.AppImage && \
    chmod +x /usr/local/bin/tools/linuxdeploy-aarch64.AppImage && \
    wget -q -O /usr/local/bin/tools/appimagetool-x86_64.AppImage https://github.com/AppImage/appimagetool/releases/download/continuous/appimagetool-x86_64.AppImage && \
    chmod +x /usr/local/bin/tools/appimagetool-x86_64.AppImage && \
    # Obtain runtime-aarch64
    wget -q -O /tmp/appimagetool-aarch64.AppImage https://github.com/AppImage/appimagetool/releases/download/continuous/appimagetool-aarch64.AppImage && \
    # Verify Checksum
    echo "1b00524ba8c6b678dc15ef88a5c25ec24def36cdfc7e3abb32ddcd068e8007fe  /tmp/appimagetool-aarch64.AppImage" | sha256sum -c - && \
    chmod +x /tmp/appimagetool-aarch64.AppImage && \
    OFFSET=$(qemu-aarch64-static /tmp/appimagetool-aarch64.AppImage --appimage-offset) && \
    dd if=/tmp/appimagetool-aarch64.AppImage of=/usr/local/bin/tools/runtime-aarch64 bs=1 count=$OFFSET && \
    chmod +x /usr/local/bin/tools/runtime-aarch64 && \
    rm -rf /tmp/appimagetool-aarch64.AppImage

ENV PATH="/usr/local/bin/tools:${PATH}"
ENV LINUXDEPLOY_BIN="/usr/local/bin/tools/linuxdeploy-aarch64.AppImage"
ENV APPIMAGETOOL_BIN="/usr/local/bin/tools/appimagetool-x86_64.AppImage"
ENV RUNTIME_BIN="/usr/local/bin/tools/runtime-aarch64"

# Build Environment Variables
ENV GOOS=linux
ENV GOARCH=arm64
ENV CC=aarch64-linux-gnu-gcc
ENV CGO_ENABLED=1
ENV CROSS_PKG_CONFIG_DIR=""
ENV CROSS_PKG_CONFIG_LIBDIR="/sysroot/arm64/usr/lib/aarch64-linux-gnu/pkgconfig:/sysroot/arm64/usr/share/pkgconfig"
ENV CROSS_PKG_CONFIG_SYSROOT_DIR="/sysroot/arm64"
ENV CROSS_CGO_CFLAGS="--sysroot=/sysroot/arm64"
ENV CROSS_CGO_LDFLAGS="--sysroot=/sysroot/arm64"

WORKDIR /build
