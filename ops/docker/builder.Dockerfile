# -----------------------------------------------------------------------------
# Stage 1: Generate ARM64 Sysroot
# -----------------------------------------------------------------------------
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
# Stage 2: Final Builder
# -----------------------------------------------------------------------------
# -----------------------------------------------------------------------------
# Stage 2: Final Builder
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

ENV GOPATH=/go
ENV PATH=$GOPATH/bin:/usr/local/go/bin:$PATH

RUN mkdir -p "$GOPATH/src" "$GOPATH/bin" && chmod -R 777 "$GOPATH"

# Install basic utils (AMD64 default) & Multi-arch setup
RUN dpkg --add-architecture arm64 && \
    apt-get update && apt-get install -y \
    curl \
    wget \
    git \
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

# Copy ARM64 Sysroot from Stage 1
# We copy specific directories to avoid overwriting host (amd64) headers if we used /usr directly
COPY --from=sysroot-gen /usr/lib/aarch64-linux-gnu /sysroot/arm64/usr/lib/aarch64-linux-gnu
COPY --from=sysroot-gen /usr/include /sysroot/arm64/usr/include
COPY --from=sysroot-gen /usr/share/pkgconfig /sysroot/arm64/usr/share/pkgconfig
# Some libs might be in /lib/aarch64-linux-gnu (though less likely for dev packages)
COPY --from=sysroot-gen /lib/aarch64-linux-gnu /sysroot/arm64/lib/aarch64-linux-gnu
# Ensure dynamic linker is present (linker often looks for absolute path /lib/ld-linux-aarch64.so.1)
COPY --from=sysroot-gen /lib/ld-linux-aarch64.so.1 /sysroot/arm64/lib/ld-linux-aarch64.so.1

# ALSO copy them to system paths so qemu/linuxdeploy can find them
COPY --from=sysroot-gen /usr/lib/aarch64-linux-gnu /usr/lib/aarch64-linux-gnu
COPY --from=sysroot-gen /lib/aarch64-linux-gnu /lib/aarch64-linux-gnu

# Symlink essential generic includes to sysroot if needed, or rely on --sysroot logic
# Usually --sysroot=/sysroot/arm64 will expect /sysroot/arm64/usr/include

# Install Node.js 20
RUN curl -fsSL https://deb.nodesource.com/setup_20.x | bash - && \
    apt-get install -y nodejs && \
    npm install -g yarn

# Install Zig 0.13.0
RUN cd /tmp && \
    wget -q https://ziglang.org/download/0.13.0/zig-linux-x86_64-0.13.0.tar.xz && \
    tar -xf zig-linux-x86_64-0.13.0.tar.xz && \
    mv zig-linux-x86_64-0.13.0 /opt/zig && \
    rm zig-linux-x86_64-0.13.0.tar.xz

ENV PATH="/opt/zig:${PATH}"

# Install Wails
RUN go install github.com/wailsapp/wails/v2/cmd/wails@v2.11.0

# -----------------------------------------------------------------------------
# Install Build Tools (Pre-baked to avoid network flakes in CI)
# -----------------------------------------------------------------------------
RUN mkdir -p /usr/local/bin/tools && \
    # linuxdeploy x86_64 (Pinned Version)
    wget -q -O /usr/local/bin/tools/linuxdeploy-x86_64.AppImage https://github.com/linuxdeploy/linuxdeploy/releases/download/continuous/linuxdeploy-x86_64.AppImage && \
    chmod +x /usr/local/bin/tools/linuxdeploy-x86_64.AppImage && \
    # linuxdeploy arm64 (Pinned Version)
    wget -q -O /usr/local/bin/tools/linuxdeploy-aarch64.AppImage https://github.com/linuxdeploy/linuxdeploy/releases/download/continuous/linuxdeploy-aarch64.AppImage && \
    chmod +x /usr/local/bin/tools/linuxdeploy-aarch64.AppImage && \
    # appimagetool x86_64 (Continuous)
    wget -q -O /usr/local/bin/tools/appimagetool-x86_64.AppImage https://github.com/AppImage/appimagetool/releases/download/continuous/appimagetool-x86_64.AppImage && \
    chmod +x /usr/local/bin/tools/appimagetool-x86_64.AppImage && \
    # Obtain runtime-aarch64 by extracting it from appimagetool-aarch64 using QEMU
    wget -q -O /tmp/appimagetool-aarch64.AppImage https://github.com/AppImage/appimagetool/releases/download/continuous/appimagetool-aarch64.AppImage && \
    # Verify Checksum (User provided: 1b00524ba8c6b678dc15ef88a5c25ec24def36cdfc7e3abb32ddcd068e8007fe)
    echo "1b00524ba8c6b678dc15ef88a5c25ec24def36cdfc7e3abb32ddcd068e8007fe  /tmp/appimagetool-aarch64.AppImage" | sha256sum -c - && \
    chmod +x /tmp/appimagetool-aarch64.AppImage && \
    cd /tmp && \
    # Attempt extraction
    # The runtime is the EXEC HEADER of the AppImage, not a file inside it.
    # We must calculate the offset of the squashfs and simple dd the header.
    OFFSET=$(qemu-aarch64-static ./appimagetool-aarch64.AppImage --appimage-offset) && \
    dd if=./appimagetool-aarch64.AppImage of=/usr/local/bin/tools/runtime-aarch64 bs=1 count=$OFFSET && \
    chmod +x /usr/local/bin/tools/runtime-aarch64 && \
    rm -rf /tmp/appimagetool-aarch64.AppImage /tmp/squashfs-root


ENV PATH="/usr/local/bin/tools:${PATH}"


WORKDIR /build
