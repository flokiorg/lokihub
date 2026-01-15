$ErrorActionPreference = "Stop"

# Debug information
Write-Host "Windows Build Script Started."
$env:TAG = $env:TAG
if (-not $env:TAG) {
    if (Test-Path "VERSION") {
        $env:TAG = Get-Content "VERSION"
    } else {
        Write-Error "TAG environment variable is required."
        exit 1
    }
}
# Use BUILD_TAG for internal version string if provided, otherwise default to TAG
$env:VERSION_STRING = if ($env:BUILD_TAG) { $env:BUILD_TAG } else { $env:TAG }

Write-Host "TAG=$env:TAG"
Write-Host "VERSION_STRING=$env:VERSION_STRING"

New-Item -ItemType Directory -Force -Path "ops/bin" | Out-Null
New-Item -ItemType Directory -Force -Path "build" | Out-Null

# -----------------------------------------------------------------------------
# 1. Frontend Build (Shared)
# -----------------------------------------------------------------------------
Write-Host "--- Building Frontend (HTTP) ---"
Push-Location "frontend"
try {
    yarn install
    if ($LASTEXITCODE -ne 0) { throw "yarn install failed" }
    
    # DEBUG: Check wailsjs existence
    Write-Host "--- Debug: Listing wailsjs directory ---"
    if (Test-Path "wailsjs") {
        Get-ChildItem -Recurse "wailsjs" | Select-Object FullName
        Write-Warning "wailsjs directory not found (expected if fresh checkout)"
    }

    yarn build:http
    if ($LASTEXITCODE -ne 0) { throw "yarn build:http failed" }
} finally {
    Pop-Location
}

# -----------------------------------------------------------------------------
# 2. Build HTTP Server (Native)
# -----------------------------------------------------------------------------
Write-Host "--- Building HTTP Servers ---"

function Build-Http {
    param (
        [string]$Arch
    )
    $OutputName = "lokihub-http-windows-${Arch}.exe"
    $TargetBinaryName = "lokihub.exe"
    $ArchiveName = "lokihub-server-windows-${env:TAG}.zip"
    
    Write-Host "Building HTTP for windows/$Arch..."
    
    $env:CGO_ENABLED = "1"
    $env:GOOS = "windows"
    $env:GOARCH = $Arch
    
    go build -a -trimpath -ldflags "-s -w -X 'github.com/flokiorg/lokihub/pkg/version.Tag=$($env:VERSION_STRING)'" `
        -o "ops/bin/$OutputName" ./cmd/http

    if ($LASTEXITCODE -ne 0) { throw "go build failed" }

    Push-Location "ops/bin"
    try {
        Move-Item -Force $OutputName $TargetBinaryName
        Compress-Archive -Path $TargetBinaryName -DestinationPath $ArchiveName -Force
        Remove-Item $TargetBinaryName
    } finally {
        Pop-Location
    }
}

# Build AMD64 HTTP
Build-Http "amd64"
# Build ARM64 HTTP (if supported/needed, skipping for now as typical Windows servers are x64)
# Build-Http "arm64" 

# -----------------------------------------------------------------------------
# 3. Build Desktop (Native)
# -----------------------------------------------------------------------------
Write-Host "--- Building Desktop Apps ---"

# Setup Wails Assets
Copy-Item "ops/appicon.png" "build/" -Force
if (Test-Path "ops/windows") { Copy-Item -Recurse "ops/windows" "build/" -Force }

function Build-Desktop {
    param (
        [string]$Arch
    )
    $WailsPlatform = "windows/$Arch"
    $Basename = "lokihub-desktop-windows"
    $ArchiveName = "lokihub-desktop-windows-${env:TAG}.zip"

    Write-Host "Building Desktop for $Arch..."

    # Native Windows Build - No Zig needed!
    # Wails handles details (using gcc/mingw if CGO needed, or MSVC)
    
    # We explicitly force windowsgui via Wails default behavior
    # LDFLAGS only needs version info now
    $LdFlags = "-X 'github.com/flokiorg/lokihub/pkg/version.Tag=$($env:VERSION_STRING)'"

    # 1. Enforce Clean Build
    if (Test-Path "build/bin") {
        Remove-Item -Recurse -Force "build/bin"
    }

    wails build -platform $WailsPlatform -webview2 embed -tags wails `
        -ldflags $LdFlags `
        -o "${Basename}.exe" -clean

    if ($LASTEXITCODE -ne 0) { throw "wails build failed" }

    # Packaging
    Push-Location "ops/bin"
    try {
        $SourceBin = "../../build/bin/${Basename}.exe"
        if (Test-Path $SourceBin) {
            Move-Item -Force $SourceBin "lokihub.exe"
            Compress-Archive -Path "lokihub.exe" -DestinationPath $ArchiveName -Force
            Remove-Item "lokihub.exe"
        } else {
            Write-Warning "Output $SourceBin not found"
        }
    } finally {
        Pop-Location
    }
}

# Build Windows AMD64 Desktop
Build-Desktop "amd64"

# Build Windows ARM64 Desktop
# Build Windows ARM64 Desktop
# Build-Desktop "arm64"

Write-Host "Windows Build Script Complete."
Get-ChildItem "ops/bin" | Select-Object Name, Length
