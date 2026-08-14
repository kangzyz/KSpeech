[CmdletBinding()]
param(
    [string]$PayloadDirectory = "publish",
    [string]$OutputDirectory = "build\installer",
    [string]$Version = "",
    [string]$Commit = "",
    [string]$IsccPath = ""
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

# The payload manifest mirrors $requiredPublishFiles in build-windows.ps1. A
# publish directory that satisfies that script satisfies this one.
$requiredPayloadFiles = @(
    "KSpeech.exe",
    "sherpa-onnx-c-api.dll",
    "onnxruntime.dll",
    "kaldi-native-fbank-core.dll",
    "ncnn.dll",
    "sherpa-ncnn-c-api.dll",
    "sherpa-ncnn-core.dll",
    "MSVCP140.dll",
    "VCRUNTIME140.dll",
    "VCRUNTIME140_1.dll",
    "VCOMP140.dll",
    "hotwords.txt",
    "itn_zh_number.fst"
)

function Invoke-Checked {
    param(
        [Parameter(Mandatory)][string]$FilePath,
        [Parameter(Mandatory)][string[]]$Arguments,
        [Parameter(Mandatory)][string]$Description
    )

    Write-Host "==> $Description"
    & $FilePath @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "$Description failed with exit code $LASTEXITCODE."
    }
}

function Invoke-Captured {
    param(
        [Parameter(Mandatory)][string]$FilePath,
        [Parameter(Mandatory)][string[]]$Arguments,
        [Parameter(Mandatory)][string]$Description
    )

    $output = & $FilePath @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "$Description failed with exit code $LASTEXITCODE."
    }
    return ($output -join "`n").Trim()
}

function Resolve-Iscc {
    param([string]$Explicit)

    $candidates = New-Object System.Collections.Generic.List[string]
    if (-not [string]::IsNullOrWhiteSpace($Explicit)) {
        $candidates.Add($Explicit)
    }
    if (-not [string]::IsNullOrWhiteSpace($env:KSPEECH_ISCC)) {
        $candidates.Add($env:KSPEECH_ISCC)
    }

    $onPath = Get-Command "ISCC.exe" -ErrorAction SilentlyContinue
    if ($null -ne $onPath) {
        $candidates.Add($onPath.Source)
    }

    # Inno Setup records its install location under the 32-bit uninstall hive.
    $registryKeys = @(
        "HKLM:\SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\Inno Setup 6_is1",
        "HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\Inno Setup 6_is1",
        "HKCU:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\Inno Setup 6_is1"
    )
    foreach ($key in $registryKeys) {
        $properties = Get-ItemProperty -Path $key -ErrorAction SilentlyContinue
        if ($null -eq $properties) {
            continue
        }
        foreach ($valueName in @("InstallLocation", "Inno Setup: App Path")) {
            if ($properties.PSObject.Properties.Name -notcontains $valueName) {
                continue
            }
            $installLocation = $properties.$valueName
            if (-not [string]::IsNullOrWhiteSpace($installLocation)) {
                $candidates.Add((Join-Path $installLocation "ISCC.exe"))
            }
        }
    }

    foreach ($programFiles in @(${env:ProgramFiles(x86)}, $env:ProgramFiles)) {
        if (-not [string]::IsNullOrWhiteSpace($programFiles)) {
            $candidates.Add((Join-Path $programFiles "Inno Setup 6\ISCC.exe"))
        }
    }

    foreach ($candidate in $candidates) {
        if (Test-Path -LiteralPath $candidate -PathType Leaf) {
            return [System.IO.Path]::GetFullPath($candidate)
        }
    }

    throw "ISCC.exe was not found. Install Inno Setup 6.3 or newer, or pass -IsccPath."
}

function Get-VersionInfoVersion {
    param([Parameter(Mandatory)][string]$Version)

    # Inno Setup only accepts numeric version info, so a tag such as
    # v0.1.0-3-gabc1234 contributes 0.1.0.0 and nothing else.
    $match = [regex]::Match($Version, '^[vV]?(\d+)(?:\.(\d+))?(?:\.(\d+))?(?:\.(\d+))?')
    if (-not $match.Success) {
        return "0.0.0.0"
    }

    $parts = @()
    for ($index = 1; $index -le 4; $index++) {
        $group = $match.Groups[$index]
        $parts += if ($group.Success) { $group.Value } else { "0" }
    }
    return ($parts -join ".")
}

$repositoryRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
$scriptPath = Join-Path $repositoryRoot "installer\kspeech.iss"
if (-not (Test-Path -LiteralPath $scriptPath -PathType Leaf)) {
    throw "Installer script was not found: $scriptPath"
}

if ([System.IO.Path]::IsPathRooted($PayloadDirectory)) {
    $payloadPath = [System.IO.Path]::GetFullPath($PayloadDirectory)
} else {
    $payloadPath = [System.IO.Path]::GetFullPath((Join-Path $repositoryRoot $PayloadDirectory))
}
if (-not (Test-Path -LiteralPath $payloadPath -PathType Container)) {
    throw "Payload directory was not found: $payloadPath"
}

if ([System.IO.Path]::IsPathRooted($OutputDirectory)) {
    $outputPath = [System.IO.Path]::GetFullPath($OutputDirectory)
} else {
    $outputPath = [System.IO.Path]::GetFullPath((Join-Path $repositoryRoot $OutputDirectory))
}

foreach ($fileName in $requiredPayloadFiles) {
    $filePath = Join-Path $payloadPath $fileName
    if (-not (Test-Path -LiteralPath $filePath -PathType Leaf)) {
        throw "Payload is missing a required file: $filePath"
    }
    if ((Get-Item -LiteralPath $filePath -Force).Length -le 0) {
        throw "Payload file is empty: $filePath"
    }
}

$iscc = Resolve-Iscc -Explicit $IsccPath

if ([string]::IsNullOrWhiteSpace($Version) -or [string]::IsNullOrWhiteSpace($Commit)) {
    $git = (Get-Command "git" -ErrorAction SilentlyContinue)
    if ($null -eq $git) {
        throw "git was not found in PATH; pass -Version and -Commit explicitly."
    }
    Push-Location $repositoryRoot
    try {
        if ([string]::IsNullOrWhiteSpace($Version)) {
            $Version = Invoke-Captured $git.Source @("describe", "--tags", "--always") "Determine installer version"
        }
        if ([string]::IsNullOrWhiteSpace($Commit)) {
            $Commit = Invoke-Captured $git.Source @("rev-parse", "--short=12", "HEAD") "Determine installer commit"
        }
    } finally {
        Pop-Location
    }
}

$versionInfoVersion = Get-VersionInfoVersion -Version $Version
$outputBaseName = "KSpeech-$Version-win-x64-setup"
$outputFile = Join-Path $outputPath "$outputBaseName.exe"

if (-not (Test-Path -LiteralPath $outputPath -PathType Container)) {
    [System.IO.Directory]::CreateDirectory($outputPath) | Out-Null
}

Write-Host "==> Compiler: $iscc"
Write-Host "==> Payload: $payloadPath"
Write-Host "==> Version: $Version ($Commit), version info $versionInfoVersion"

Invoke-Checked $iscc @(
    "/DAppVersion=$Version",
    "/DVersionInfoVersion=$versionInfoVersion",
    "/DAppCommit=$Commit",
    "/DPayloadDir=$payloadPath",
    "/DOutputDir=$outputPath",
    "/DOutputBaseName=$outputBaseName",
    $scriptPath
) "Compile Windows installer"

if (-not (Test-Path -LiteralPath $outputFile -PathType Leaf)) {
    throw "The compiler reported success but the installer is missing: $outputFile"
}

$installer = Get-Item -LiteralPath $outputFile -Force
$hash = [Convert]::ToHexString(
    [System.Security.Cryptography.SHA256]::HashData([System.IO.File]::ReadAllBytes($outputFile))
)

Write-Host
Write-Host "==> Installer: $($installer.FullName)"
Write-Host "==> Size: $([math]::Round($installer.Length / 1MB, 2)) MB"
Write-Host "==> SHA256: $hash"
