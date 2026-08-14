[CmdletBinding()]
param(
    [string]$PublishDirectory = "publish",
    [string]$Version = "",
    [string]$Commit = ""
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

function Get-RequiredCommand {
    param([Parameter(Mandatory)][string]$Name)

    $command = Get-Command $Name -ErrorAction SilentlyContinue
    if ($null -eq $command) {
        throw "Required command '$Name' was not found in PATH."
    }
    return $command.Source
}

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

$managedMarkerName = ".kspeech-build-output"
$managedMarkerContent = "KSpeech build-windows.ps1 managed output v1"
# Only a directory carrying one of these markers may be adopted for repair and
# rollback. The list stays a collection so a future marker revision can accept
# the previous one; only the first entry is ever written.
$managedMarkers = @(
    [pscustomobject]@{ Name = $managedMarkerName; Content = $managedMarkerContent }
)
$realModelGateMarkerName = ".kspeech-real-model-gate"
$realModelGateMarkerContent = "KSpeech build-windows.ps1 real-model gate v1"
$realModelGateDirectoryPrefix = "KSpeech.real-model-gate."

function Resolve-RealModelRoot {
    param([Parameter(Mandatory)][string]$Path)

    $candidate = $Path.Trim()
    if ([string]::IsNullOrWhiteSpace($candidate)) {
        throw "KSPEECH_REAL_MODEL_ROOT must not be blank when the real-model gate is enabled."
    }
    if (-not [System.IO.Path]::IsPathRooted($candidate)) {
        throw "KSPEECH_REAL_MODEL_ROOT must be an absolute path: $candidate"
    }

    try {
        $item = Get-Item -LiteralPath $candidate -Force -ErrorAction Stop
    } catch {
        throw "KSPEECH_REAL_MODEL_ROOT does not resolve to an existing directory: $candidate ($($_.Exception.Message))"
    }
    if ($item.PSProvider.Name -ne "FileSystem" -or -not $item.PSIsContainer) {
        throw "KSPEECH_REAL_MODEL_ROOT must resolve to a filesystem directory: $candidate"
    }

    return [System.IO.Path]::GetFullPath($item.FullName)
}

function Assert-RealModelFile {
    param(
        [Parameter(Mandatory)][string]$Root,
        [Parameter(Mandatory)][string]$RelativePath
    )

    $path = Join-Path $Root $RelativePath
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
        throw "Required real-model fixture was not found: $path"
    }
    $item = Get-Item -LiteralPath $path -Force
    if ($item.PSProvider.Name -ne "FileSystem" -or
        ($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0 -or
        $item.Length -le 0) {
        throw "Required real-model fixture is not a non-empty regular file: $path"
    }
}

function Assert-RealModelLayout {
    param([Parameter(Mandatory)][string]$Root)

    $requiredFiles = @(
        "test.wav",
        "onnx\encoder.onnx",
        "onnx\decoder.onnx",
        "onnx\joiner.onnx",
        "onnx\tokens.txt",
        "ncnn\encoder.param",
        "ncnn\encoder.bin",
        "ncnn\decoder.param",
        "ncnn\decoder.bin",
        "ncnn\joiner.param",
        "ncnn\joiner.bin",
        "ncnn\tokens.txt"
    )
    foreach ($relativePath in $requiredFiles) {
        Assert-RealModelFile -Root $Root -RelativePath $relativePath
    }
}

function Assert-NoReparsePathComponents {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$TrustedRoot
    )

    $fullPath = [System.IO.Path]::GetFullPath($Path)
    $fullRoot = [System.IO.Path]::GetFullPath($TrustedRoot).TrimEnd(
        [System.IO.Path]::DirectorySeparatorChar,
        [System.IO.Path]::AltDirectorySeparatorChar
    )
    $rootPrefix = $fullRoot + [System.IO.Path]::DirectorySeparatorChar
    if ($fullPath -ne $fullRoot -and
        -not $fullPath.StartsWith($rootPrefix, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "Refusing a path outside its trusted root: $fullPath"
    }
    if (-not (Test-Path -LiteralPath $fullRoot)) {
        throw "Trusted filesystem root was not found: $fullRoot"
    }

    $rootItem = Get-Item -LiteralPath $fullRoot -Force
    if (($rootItem.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "Refusing a trusted root that is a reparse point: $fullRoot"
    }

    $relativePath = [System.IO.Path]::GetRelativePath($fullRoot, $fullPath)
    $currentComponent = $fullRoot
    foreach ($pathSegment in $relativePath.Split(
        [char[]]@([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar),
        [System.StringSplitOptions]::RemoveEmptyEntries
    )) {
        if ($pathSegment -eq ".") {
            continue
        }
        $currentComponent = Join-Path $currentComponent $pathSegment
        if (Test-Path -LiteralPath $currentComponent) {
            $componentItem = Get-Item -LiteralPath $currentComponent -Force
            if (($componentItem.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
                throw "Refusing a path beneath a reparse point: $currentComponent"
            }
        }
    }
}

function Assert-X64PEFile {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$TrustedRoot
    )

    Assert-NoReparsePathComponents -Path $Path -TrustedRoot $TrustedRoot
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw "Required x64 PE file was not found: $Path"
    }
    $item = Get-Item -LiteralPath $Path -Force
    if (($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0 -or $item.Length -le 0) {
        throw "Required x64 PE file is unsafe or empty: $Path"
    }

    $stream = $null
    $reader = $null
    try {
        $stream = [System.IO.File]::Open(
            $item.FullName,
            [System.IO.FileMode]::Open,
            [System.IO.FileAccess]::Read,
            [System.IO.FileShare]::Read
        )
        $reader = [System.IO.BinaryReader]::new($stream)
        if ($stream.Length -lt 64 -or $reader.ReadUInt16() -ne 0x5A4D) {
            throw "Required runtime is not a valid PE file: $Path"
        }
        $stream.Position = 0x3C
        $peOffset = $reader.ReadInt32()
        if ($peOffset -lt 0 -or ([int64]$peOffset + 26) -gt $stream.Length) {
            throw "Required runtime has an invalid PE header offset: $Path"
        }
        $stream.Position = $peOffset
        if ($reader.ReadUInt32() -ne 0x00004550) {
            throw "Required runtime has an invalid PE signature: $Path"
        }
        $machine = $reader.ReadUInt16()
        if ($machine -ne 0x8664) {
            throw ("Required runtime is not x64 (machine 0x{0:X4}): {1}" -f $machine, $Path)
        }
        $stream.Position = $peOffset + 24
        $optionalHeaderMagic = $reader.ReadUInt16()
        if ($optionalHeaderMagic -ne 0x020B) {
            throw ("Required runtime is not PE32+ (magic 0x{0:X4}): {1}" -f $optionalHeaderMagic, $Path)
        }
    } finally {
        if ($null -ne $reader) {
            $reader.Dispose()
        } elseif ($null -ne $stream) {
            $stream.Dispose()
        }
    }
}

function New-MicrosoftRuntimeRecord {
    param(
        [Parameter(Mandatory)][string]$Name,
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$TrustedRoot,
        [Parameter(Mandatory)][string]$Source
    )

    Assert-X64PEFile -Path $Path -TrustedRoot $TrustedRoot
    $item = Get-Item -LiteralPath $Path -Force
    $fileVersion = $item.VersionInfo.FileVersion
    if ([string]::IsNullOrWhiteSpace($fileVersion)) {
        throw "Microsoft runtime has no file-version metadata: $Path"
    }
    return [pscustomobject]@{
        Name = $Name
        Path = $item.FullName
        TrustedRoot = [System.IO.Path]::GetFullPath($TrustedRoot)
        Source = $Source
        Version = $fileVersion
    }
}

function Get-VisualStudioRedistCandidates {
    $programFilesRoots = @(
        [Environment]::GetFolderPath([Environment+SpecialFolder]::ProgramFiles),
        [Environment]::GetFolderPath([Environment+SpecialFolder]::ProgramFilesX86)
    )
    $seenRoots = [System.Collections.Generic.HashSet[string]]::new(
        [System.StringComparer]::OrdinalIgnoreCase
    )
    $candidates = @()
    foreach ($programFilesRoot in $programFilesRoots) {
        if ([string]::IsNullOrWhiteSpace($programFilesRoot)) {
            continue
        }
        $programFilesRoot = [System.IO.Path]::GetFullPath($programFilesRoot)
        if (-not $seenRoots.Add($programFilesRoot)) {
            continue
        }
        $visualStudioRoot = Join-Path $programFilesRoot "Microsoft Visual Studio"
        if (-not (Test-Path -LiteralPath $visualStudioRoot -PathType Container)) {
            continue
        }
        Assert-NoReparsePathComponents -Path $visualStudioRoot -TrustedRoot $programFilesRoot

        foreach ($yearDirectory in @(Get-ChildItem -LiteralPath $visualStudioRoot -Directory -Force)) {
            if (($yearDirectory.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
                continue
            }
            foreach ($editionDirectory in @(Get-ChildItem -LiteralPath $yearDirectory.FullName -Directory -Force)) {
                if (($editionDirectory.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
                    continue
                }
                $msvcRedistRoot = Join-Path $editionDirectory.FullName "VC\Redist\MSVC"
                if (-not (Test-Path -LiteralPath $msvcRedistRoot -PathType Container)) {
                    continue
                }
                Assert-NoReparsePathComponents -Path $msvcRedistRoot -TrustedRoot $visualStudioRoot
                foreach ($versionDirectory in @(Get-ChildItem -LiteralPath $msvcRedistRoot -Directory -Force)) {
                    if (($versionDirectory.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
                        continue
                    }
                    try {
                        $parsedVersion = [Version]$versionDirectory.Name
                    } catch {
                        continue
                    }
                    $candidates += [pscustomobject]@{
                        Root = $versionDirectory.FullName
                        TrustedRoot = $visualStudioRoot
                        Version = $parsedVersion
                    }
                }
            }
        }
    }
    return @($candidates | Sort-Object Version -Descending)
}

function Get-VisualStudioRuntimeBundle {
    param(
        [Parameter(Mandatory)]$Candidate,
        [Parameter(Mandatory)][string[]]$DLLNames
    )

    $x64Root = Join-Path $Candidate.Root "x64"
    if (-not (Test-Path -LiteralPath $x64Root -PathType Container)) {
        return $null
    }
    Assert-NoReparsePathComponents -Path $x64Root -TrustedRoot $Candidate.TrustedRoot
    $runtimeDirectories = @(Get-ChildItem -LiteralPath $x64Root -Directory -Force)
    foreach ($runtimeDirectory in $runtimeDirectories) {
        if (($runtimeDirectory.Name -match '^Microsoft\.VC\d+\.(CRT|OpenMP)$') -and
            ($runtimeDirectory.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "Refusing a Visual Studio runtime directory that is a reparse point: $($runtimeDirectory.FullName)"
        }
    }
    $crtDirectories = @(
        $runtimeDirectories |
            Where-Object { $_.Name -match '^Microsoft\.VC\d+\.CRT$' } |
            Sort-Object Name -Descending
    )
    $openMPDirectories = @(
        $runtimeDirectories |
            Where-Object { $_.Name -match '^Microsoft\.VC\d+\.OpenMP$' } |
            Sort-Object Name -Descending
    )
    $crtDLLNames = @($DLLNames | Where-Object { $_ -ne "VCOMP140.dll" })
    $crtDirectory = $crtDirectories | Where-Object {
        $directory = $_.FullName
        @($crtDLLNames | Where-Object {
            -not (Test-Path -LiteralPath (Join-Path $directory $_) -PathType Leaf)
        }).Count -eq 0
    } | Select-Object -First 1
    $openMPDirectory = $openMPDirectories | Where-Object {
        Test-Path -LiteralPath (Join-Path $_.FullName "VCOMP140.dll") -PathType Leaf
    } | Select-Object -First 1
    if ($null -eq $crtDirectory -or $null -eq $openMPDirectory) {
        return $null
    }

    $records = [ordered]@{}
    foreach ($dllName in $DLLNames) {
        $sourceDirectory = if ($dllName -eq "VCOMP140.dll") {
            $openMPDirectory.FullName
        } else {
            $crtDirectory.FullName
        }
        $records[$dllName] = New-MicrosoftRuntimeRecord `
            -Name $dllName `
            -Path (Join-Path $sourceDirectory $dllName) `
            -TrustedRoot $Candidate.TrustedRoot `
            -Source "Visual Studio Redist $($Candidate.Version)"
    }
    return [pscustomobject]@{
        Source = "Visual Studio Redist $($Candidate.Version)"
        Files = $records
    }
}

function Get-MicrosoftRuntimeBundle {
    param([Parameter(Mandatory)][string[]]$DLLNames)

    foreach ($candidate in @(Get-VisualStudioRedistCandidates)) {
        $bundle = Get-VisualStudioRuntimeBundle -Candidate $candidate -DLLNames $DLLNames
        if ($null -ne $bundle) {
            return $bundle
        }
    }

    $systemDirectory = [System.IO.Path]::GetFullPath([Environment]::SystemDirectory)
    if (-not (Test-Path -LiteralPath $systemDirectory -PathType Container)) {
        throw "Windows System32 directory was not found: $systemDirectory"
    }
    $records = [ordered]@{}
    foreach ($dllName in $DLLNames) {
        $path = Join-Path $systemDirectory $dllName
        if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
            throw "Microsoft x64 runtime was not found in a complete Visual Studio Redist bundle or System32: $dllName"
        }
        $records[$dllName] = New-MicrosoftRuntimeRecord `
            -Name $dllName `
            -Path $path `
            -TrustedRoot $systemDirectory `
            -Source "Windows System32 fallback"
    }
    return [pscustomobject]@{
        Source = "Windows System32 fallback"
        Files = $records
    }
}

function Copy-MicrosoftRuntimeBundle {
    param(
        [Parameter(Mandatory)]$Bundle,
        [Parameter(Mandatory)][string]$DestinationDirectory
    )

    foreach ($dllName in $Bundle.Files.Keys) {
        $record = $Bundle.Files[$dllName]
        Assert-X64PEFile -Path $record.Path -TrustedRoot $record.TrustedRoot
        $destinationPath = Join-Path $DestinationDirectory $dllName
        Copy-Item -LiteralPath $record.Path -Destination $destinationPath -Force
        Assert-X64PEFile -Path $destinationPath -TrustedRoot $DestinationDirectory

        $sourceHash = [Convert]::ToHexString(
            [System.Security.Cryptography.SHA256]::HashData([System.IO.File]::ReadAllBytes($record.Path))
        )
        $destinationHash = [Convert]::ToHexString(
            [System.Security.Cryptography.SHA256]::HashData([System.IO.File]::ReadAllBytes($destinationPath))
        )
        if ($sourceHash -cne $destinationHash) {
            throw "Microsoft runtime copy verification failed: $dllName"
        }
    }
}

function Assert-RealModelGateDirectory {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$ExpectedParent
    )

    $fullPath = [System.IO.Path]::GetFullPath($Path)
    $fullParent = [System.IO.Path]::GetFullPath($ExpectedParent).TrimEnd(
        [System.IO.Path]::DirectorySeparatorChar,
        [System.IO.Path]::AltDirectorySeparatorChar
    )
    $actualParent = [System.IO.Path]::GetFullPath((Split-Path -Parent $fullPath)).TrimEnd(
        [System.IO.Path]::DirectorySeparatorChar,
        [System.IO.Path]::AltDirectorySeparatorChar
    )
    if ($actualParent -ne $fullParent) {
        throw "Refusing a real-model gate directory outside its expected temporary parent: $fullPath"
    }
    $leaf = [System.IO.Path]::GetFileName($fullPath)
    if (-not $leaf.StartsWith($realModelGateDirectoryPrefix, [System.StringComparison]::Ordinal)) {
        throw "Refusing an unexpected real-model gate directory: $fullPath"
    }
    if (-not (Test-Path -LiteralPath $fullPath -PathType Container)) {
        throw "Real-model gate directory was not found: $fullPath"
    }
    $directoryItem = Get-Item -LiteralPath $fullPath -Force
    if (($directoryItem.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "Refusing a real-model gate directory that is a reparse point: $fullPath"
    }

    $markerPath = Join-Path $fullPath $realModelGateMarkerName
    if (-not (Test-Path -LiteralPath $markerPath -PathType Leaf)) {
        throw "Refusing an unmarked real-model gate directory: $fullPath"
    }
    $markerItem = Get-Item -LiteralPath $markerPath -Force
    if (($markerItem.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0 -or
        [System.IO.File]::ReadAllText($markerPath) -cne $realModelGateMarkerContent) {
        throw "Refusing a real-model gate directory with an invalid marker: $fullPath"
    }
}

function New-RealModelGateDirectory {
    $temporaryParent = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath()).TrimEnd(
        [System.IO.Path]::DirectorySeparatorChar,
        [System.IO.Path]::AltDirectorySeparatorChar
    )
    $temporaryParentItem = Get-Item -LiteralPath $temporaryParent -Force
    if ($temporaryParentItem.PSProvider.Name -ne "FileSystem" -or -not $temporaryParentItem.PSIsContainer) {
        throw "The process temporary path is not a filesystem directory: $temporaryParent"
    }

    $path = Join-Path $temporaryParent ($realModelGateDirectoryPrefix + [Guid]::NewGuid().ToString("N"))
    if (Test-Path -LiteralPath $path) {
        throw "Refusing to reuse an existing real-model gate directory: $path"
    }
    [System.IO.Directory]::CreateDirectory($path) | Out-Null
    $utf8NoBom = [System.Text.UTF8Encoding]::new($false)
    [System.IO.File]::WriteAllText(
        (Join-Path $path $realModelGateMarkerName),
        $realModelGateMarkerContent,
        $utf8NoBom
    )
    Assert-RealModelGateDirectory -Path $path -ExpectedParent $temporaryParent
    return $path
}

function Remove-RealModelGateDirectory {
    param([Parameter(Mandatory)][string]$Path)

    $temporaryParent = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath()).TrimEnd(
        [System.IO.Path]::DirectorySeparatorChar,
        [System.IO.Path]::AltDirectorySeparatorChar
    )
    Assert-RealModelGateDirectory -Path $Path -ExpectedParent $temporaryParent
    Remove-Item -LiteralPath $Path -Recurse -Force
}

function Copy-RealModelRuntime {
    param(
        [Parameter(Mandatory)][string]$SourceDirectory,
        [Parameter(Mandatory)][string[]]$DLLNames,
        [Parameter(Mandatory)][string]$DestinationDirectory
    )

    foreach ($dllName in $DLLNames) {
        $sourcePath = Join-Path $SourceDirectory $dllName
        $destinationPath = Join-Path $DestinationDirectory $dllName
        Copy-Item -LiteralPath $sourcePath -Destination $destinationPath -Force
        $destinationItem = Get-Item -LiteralPath $destinationPath -Force
        if (($destinationItem.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0 -or
            $destinationItem.Length -le 0) {
            throw "Real-model gate runtime DLL is unsafe or empty: $destinationPath"
        }
    }
}

function Invoke-RealModelRecognitionGate {
    param(
        [Parameter(Mandatory)][string]$Go,
        [Parameter(Mandatory)][string]$ModelRoot,
        [Parameter(Mandatory)][string]$SherpaDLLDirectory,
        [Parameter(Mandatory)][string[]]$OnnxDLLs,
        [Parameter(Mandatory)][string]$SherpaNcnnDLLDirectory,
        [Parameter(Mandatory)][string[]]$NcnnDLLs,
        [Parameter(Mandatory)]$MicrosoftRuntimeBundle
    )

    Assert-RealModelLayout -Root $ModelRoot
    $gateDirectory = New-RealModelGateDirectory
    try {
        $onnxDirectory = Join-Path $gateDirectory "onnx"
        $ncnnDirectory = Join-Path $gateDirectory "ncnn"
        [System.IO.Directory]::CreateDirectory($onnxDirectory) | Out-Null
        [System.IO.Directory]::CreateDirectory($ncnnDirectory) | Out-Null

        $onnxTestExecutable = Join-Path $onnxDirectory "sherpaonnx-real.test.exe"
        $ncnnTestExecutable = Join-Path $ncnnDirectory "sherpancnn-real.test.exe"
        Invoke-Checked $Go @(
            "test", "-c", "-trimpath",
            "-tags", "production,sherpa",
            "-o", $onnxTestExecutable,
            "./internal/recognizer/sherpaonnx"
        ) "Compile isolated sherpa-onnx real-model test executable"
        Invoke-Checked $Go @(
            "test", "-c", "-trimpath",
            "-tags", "production,sherpancnn",
            "-o", $ncnnTestExecutable,
            "./internal/recognizer/sherpancnn"
        ) "Compile isolated sherpa-ncnn real-model test executable"

        Copy-RealModelRuntime `
            -SourceDirectory $SherpaDLLDirectory `
            -DLLNames $OnnxDLLs `
            -DestinationDirectory $onnxDirectory
        Copy-RealModelRuntime `
            -SourceDirectory $SherpaNcnnDLLDirectory `
            -DLLNames $NcnnDLLs `
            -DestinationDirectory $ncnnDirectory
        Copy-MicrosoftRuntimeBundle `
            -Bundle $MicrosoftRuntimeBundle `
            -DestinationDirectory $onnxDirectory
        Copy-MicrosoftRuntimeBundle `
            -Bundle $MicrosoftRuntimeBundle `
            -DestinationDirectory $ncnnDirectory

        # These executables deliberately live beside only their matching
        # official runtime DLLs. Windows therefore resolves app-local DLLs
        # before an unrelated onnxruntime.dll elsewhere on the process PATH.
        $previousRealModelRoot = [Environment]::GetEnvironmentVariable(
            "KSPEECH_REAL_MODEL_ROOT",
            [EnvironmentVariableTarget]::Process
        )
        [Environment]::SetEnvironmentVariable(
            "KSPEECH_REAL_MODEL_ROOT",
            $ModelRoot,
            [EnvironmentVariableTarget]::Process
        )
        try {
            Invoke-Checked $onnxTestExecutable @(
                "-test.run=^TestRealModelRecognition$",
                "-test.count=1",
                "-test.timeout=3m",
                "-test.v"
            ) "Run app-local sherpa-onnx real-model recognition gate"
            Invoke-Checked $ncnnTestExecutable @(
                "-test.run=^TestRealModelRecognition$",
                "-test.count=1",
                "-test.timeout=3m",
                "-test.v"
            ) "Run app-local sherpa-ncnn real-model recognition gate"
        } finally {
            [Environment]::SetEnvironmentVariable(
                "KSPEECH_REAL_MODEL_ROOT",
                $previousRealModelRoot,
                [EnvironmentVariableTarget]::Process
            )
        }
    } finally {
        if (Test-Path -LiteralPath $gateDirectory) {
            try {
                Remove-RealModelGateDirectory -Path $gateDirectory
            } catch {
                Write-Warning "Leaving real-model gate directory untouched because safe cleanup could not be verified: $gateDirectory ($($_.Exception.Message))"
            }
        }
    }
}

function Assert-NoReparseComponents {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$RepositoryRoot
    )

    $fullPath = [System.IO.Path]::GetFullPath($Path)
    $fullRoot = [System.IO.Path]::GetFullPath($RepositoryRoot)
    $rootPrefix = $fullRoot.TrimEnd(
        [System.IO.Path]::DirectorySeparatorChar,
        [System.IO.Path]::AltDirectorySeparatorChar
    ) + [System.IO.Path]::DirectorySeparatorChar
    if ($fullPath -ne $fullRoot -and -not $fullPath.StartsWith($rootPrefix, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "Refusing a path outside the repository: $fullPath"
    }

    if (Test-Path -LiteralPath $fullRoot) {
        $rootItem = Get-Item -LiteralPath $fullRoot -Force
        if (($rootItem.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "Refusing a repository reached through a reparse point: $fullRoot"
        }
    }

    $relativePath = [System.IO.Path]::GetRelativePath($fullRoot, $fullPath)
    $currentComponent = $fullRoot
    foreach ($pathSegment in $relativePath.Split(
        [char[]]@([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar),
        [System.StringSplitOptions]::RemoveEmptyEntries
    )) {
        if ($pathSegment -eq ".") {
            continue
        }
        $currentComponent = Join-Path $currentComponent $pathSegment
        if (Test-Path -LiteralPath $currentComponent) {
            $componentItem = Get-Item -LiteralPath $currentComponent -Force
            if (($componentItem.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
                throw "Refusing a path beneath a reparse point: $currentComponent"
            }
        }
    }
}

function Assert-DirectChildPath {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$ExpectedParent,
        [string]$ExpectedName = "",
        [string]$ExpectedNamePrefix = ""
    )

    $fullPath = [System.IO.Path]::GetFullPath($Path)
    $fullParent = [System.IO.Path]::GetFullPath($ExpectedParent)
    $actualParent = [System.IO.Path]::GetFullPath((Split-Path -Parent $fullPath))
    if ($actualParent -ne $fullParent) {
        throw "Refusing a transaction path outside its expected parent: $fullPath"
    }
    $name = [System.IO.Path]::GetFileName($fullPath)
    if (-not [string]::IsNullOrEmpty($ExpectedName) -and $name -cne $ExpectedName) {
        throw "Refusing an unexpected managed directory name: $fullPath"
    }
    if (-not [string]::IsNullOrEmpty($ExpectedNamePrefix) -and
        -not $name.StartsWith($ExpectedNamePrefix, [System.StringComparison]::Ordinal)) {
        throw "Refusing an unexpected transaction directory name: $fullPath"
    }
}

function Assert-ManagedDirectory {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$RepositoryRoot
    )

    Assert-NoReparseComponents -Path $Path -RepositoryRoot $RepositoryRoot
    if (-not (Test-Path -LiteralPath $Path -PathType Container)) {
        throw "Managed output directory was not found: $Path"
    }
    foreach ($marker in $managedMarkers) {
        $markerPath = Join-Path $Path $marker.Name
        if (-not (Test-Path -LiteralPath $markerPath -PathType Leaf)) {
            continue
        }
        $markerItem = Get-Item -LiteralPath $markerPath -Force
        if (($markerItem.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "Refusing a managed marker that is a reparse point: $markerPath"
        }
        if ([System.IO.File]::ReadAllText($markerPath) -cne $marker.Content) {
            throw "Refusing a directory with an invalid managed marker: $Path"
        }
        return
    }
    throw "Refusing to manage an unmarked directory: $Path"
}

function New-ManagedDirectory {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$RepositoryRoot,
        [Parameter(Mandatory)][string]$ExpectedParent,
        [Parameter(Mandatory)][string]$ExpectedNamePrefix
    )

    Assert-DirectChildPath -Path $Path -ExpectedParent $ExpectedParent -ExpectedNamePrefix $ExpectedNamePrefix
    Assert-NoReparseComponents -Path $Path -RepositoryRoot $RepositoryRoot
    if (Test-Path -LiteralPath $Path) {
        throw "Refusing to reuse an existing transaction path: $Path"
    }
    [System.IO.Directory]::CreateDirectory($Path) | Out-Null
    Assert-NoReparseComponents -Path $Path -RepositoryRoot $RepositoryRoot
    $utf8NoBom = [System.Text.UTF8Encoding]::new($false)
    [System.IO.File]::WriteAllText((Join-Path $Path $managedMarkerName), $managedMarkerContent, $utf8NoBom)
    Assert-ManagedDirectory -Path $Path -RepositoryRoot $RepositoryRoot
}

function Remove-ManagedDirectory {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$RepositoryRoot,
        [Parameter(Mandatory)][string]$ExpectedParent,
        [string]$ExpectedName = "",
        [string]$ExpectedNamePrefix = ""
    )

    Assert-DirectChildPath `
        -Path $Path `
        -ExpectedParent $ExpectedParent `
        -ExpectedName $ExpectedName `
        -ExpectedNamePrefix $ExpectedNamePrefix
    Assert-ManagedDirectory -Path $Path -RepositoryRoot $RepositoryRoot
    Remove-Item -LiteralPath $Path -Recurse -Force
}

function New-TransactionPath {
    param(
        [Parameter(Mandatory)][string]$Parent,
        [Parameter(Mandatory)][string]$Prefix
    )

    $timestamp = [DateTime]::UtcNow.ToString(
        "yyyyMMddTHHmmssfffffffZ",
        [System.Globalization.CultureInfo]::InvariantCulture
    )
    return Join-Path $Parent ($Prefix + $timestamp + "." + [Guid]::NewGuid().ToString("N"))
}

function Get-TransactionDirectories {
    param(
        [Parameter(Mandatory)][string]$Parent,
        [Parameter(Mandatory)][string]$Prefix
    )

    if (-not (Test-Path -LiteralPath $Parent -PathType Container)) {
        return @()
    }
    return @(
        Get-ChildItem -LiteralPath $Parent -Force |
            Where-Object {
                $_.PSIsContainer -and
                $_.Name.StartsWith($Prefix, [System.StringComparison]::Ordinal)
            }
    )
}

function Remove-StaleManagedDirectory {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$RepositoryRoot,
        [Parameter(Mandatory)][string]$ExpectedParent,
        [Parameter(Mandatory)][string]$ExpectedNamePrefix
    )

    try {
        Remove-ManagedDirectory `
            -Path $Path `
            -RepositoryRoot $RepositoryRoot `
            -ExpectedParent $ExpectedParent `
            -ExpectedNamePrefix $ExpectedNamePrefix
        Write-Host "==> Removed stale managed transaction $Path"
    } catch {
        Write-Warning "Leaving unmanaged or unsafe transaction path untouched: $Path ($($_.Exception.Message))"
    }
}

function Repair-ManagedPublishState {
    param(
        [Parameter(Mandatory)][string]$PublishPath,
        [Parameter(Mandatory)][string]$RepositoryRoot,
        [Parameter(Mandatory)][string]$PublishParent,
        [Parameter(Mandatory)][string]$PublishLeaf,
        [Parameter(Mandatory)][string]$StagePrefix,
        [Parameter(Mandatory)][string]$BackupPrefix,
        [Parameter(Mandatory)][string[]]$RequiredFiles
    )

    if (-not (Test-Path -LiteralPath $PublishParent -PathType Container)) {
        return
    }
    Assert-NoReparseComponents -Path $PublishParent -RepositoryRoot $RepositoryRoot

    foreach ($stage in (Get-TransactionDirectories -Parent $PublishParent -Prefix $StagePrefix)) {
        Remove-StaleManagedDirectory `
            -Path $stage.FullName `
            -RepositoryRoot $RepositoryRoot `
            -ExpectedParent $PublishParent `
            -ExpectedNamePrefix $StagePrefix
    }

    $backups = @(Get-TransactionDirectories -Parent $PublishParent -Prefix $BackupPrefix)
    if (Test-Path -LiteralPath $PublishPath) {
        Assert-DirectChildPath -Path $PublishPath -ExpectedParent $PublishParent -ExpectedName $PublishLeaf
        Assert-ManagedDirectory -Path $PublishPath -RepositoryRoot $RepositoryRoot
        Assert-PublishPayload -Path $PublishPath -RequiredFiles $RequiredFiles
        foreach ($backup in $backups) {
            Remove-StaleManagedDirectory `
                -Path $backup.FullName `
                -RepositoryRoot $RepositoryRoot `
                -ExpectedParent $PublishParent `
                -ExpectedNamePrefix $BackupPrefix
        }
        return
    }

    $managedBackups = @()
    foreach ($backup in $backups) {
        try {
            Assert-ManagedDirectory -Path $backup.FullName -RepositoryRoot $RepositoryRoot
            Assert-PublishPayload -Path $backup.FullName -RequiredFiles $RequiredFiles
            $managedBackups += $backup
        } catch {
            Write-Warning "Leaving unmanaged, incomplete, or unsafe backup untouched: $($backup.FullName) ($($_.Exception.Message))"
        }
    }
    if ($managedBackups.Count -eq 0) {
        return
    }

    $backupToRestore = $managedBackups | Sort-Object Name -Descending | Select-Object -First 1
    Move-Item -LiteralPath $backupToRestore.FullName -Destination $PublishPath
    Assert-ManagedDirectory -Path $PublishPath -RepositoryRoot $RepositoryRoot
    Assert-PublishPayload -Path $PublishPath -RequiredFiles $RequiredFiles
    Write-Host "==> Restored interrupted publish backup to $PublishPath"
    foreach ($backup in $managedBackups) {
        if ($backup.FullName -ne $backupToRestore.FullName -and (Test-Path -LiteralPath $backup.FullName)) {
            Remove-StaleManagedDirectory `
                -Path $backup.FullName `
                -RepositoryRoot $RepositoryRoot `
                -ExpectedParent $PublishParent `
                -ExpectedNamePrefix $BackupPrefix
        }
    }
}

function Assert-PublishPayload {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string[]]$RequiredFiles
    )

    foreach ($fileName in $RequiredFiles) {
        $filePath = Join-Path $Path $fileName
        if (-not (Test-Path -LiteralPath $filePath -PathType Leaf)) {
            throw "Required publish payload was not created: $filePath"
        }
        $fileItem = Get-Item -LiteralPath $filePath -Force
        if (($fileItem.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0 -or $fileItem.Length -le 0) {
            throw "Required publish payload is unsafe or empty: $filePath"
        }
    }
}

function Restore-ManagedBackup {
    param(
        [Parameter(Mandatory)][string]$PublishPath,
        [Parameter(Mandatory)][string]$BackupPath,
        [Parameter(Mandatory)][string]$RepositoryRoot,
        [Parameter(Mandatory)][string]$PublishParent,
        [Parameter(Mandatory)][string]$PublishLeaf,
        [Parameter(Mandatory)][string]$BackupPrefix
    )

    Assert-DirectChildPath -Path $BackupPath -ExpectedParent $PublishParent -ExpectedNamePrefix $BackupPrefix
    Assert-ManagedDirectory -Path $BackupPath -RepositoryRoot $RepositoryRoot
    if (Test-Path -LiteralPath $PublishPath) {
        Remove-ManagedDirectory `
            -Path $PublishPath `
            -RepositoryRoot $RepositoryRoot `
            -ExpectedParent $PublishParent `
            -ExpectedName $PublishLeaf
    }
    Move-Item -LiteralPath $BackupPath -Destination $PublishPath
    Assert-ManagedDirectory -Path $PublishPath -RepositoryRoot $RepositoryRoot
}

function Invoke-PublishSwap {
    param(
        [Parameter(Mandatory)][string]$StagingPath,
        [Parameter(Mandatory)][string]$PublishPath,
        [Parameter(Mandatory)][string]$RepositoryRoot,
        [Parameter(Mandatory)][string]$PublishParent,
        [Parameter(Mandatory)][string]$PublishLeaf,
        [Parameter(Mandatory)][string]$StagePrefix,
        [Parameter(Mandatory)][string]$BackupPrefix,
        [Parameter(Mandatory)][string[]]$RequiredFiles,
        [Parameter(Mandatory)][string[]]$PreviousRequiredFiles
    )

    Assert-DirectChildPath -Path $StagingPath -ExpectedParent $PublishParent -ExpectedNamePrefix $StagePrefix
    Assert-ManagedDirectory -Path $StagingPath -RepositoryRoot $RepositoryRoot
    Assert-PublishPayload -Path $StagingPath -RequiredFiles $RequiredFiles

    $backupPath = $null
    $backupMoved = $false
    $hadPreviousPublish = Test-Path -LiteralPath $PublishPath
    $stagingInstalled = $false
    try {
        if ($hadPreviousPublish) {
            Assert-DirectChildPath -Path $PublishPath -ExpectedParent $PublishParent -ExpectedName $PublishLeaf
            Assert-ManagedDirectory -Path $PublishPath -RepositoryRoot $RepositoryRoot
            # A managed output from an older script can legitimately have a
            # smaller runtime-DLL set. It is safe to preserve as rollback input
            # as long as its marker and executable are intact; the new staging
            # directory is still checked against the complete current payload.
            Assert-PublishPayload -Path $PublishPath -RequiredFiles $PreviousRequiredFiles
            $backupPath = New-TransactionPath -Parent $PublishParent -Prefix $BackupPrefix
            Assert-DirectChildPath -Path $backupPath -ExpectedParent $PublishParent -ExpectedNamePrefix $BackupPrefix
            Move-Item -LiteralPath $PublishPath -Destination $backupPath
            $backupMoved = $true
            Assert-ManagedDirectory -Path $backupPath -RepositoryRoot $RepositoryRoot
        }

        Move-Item -LiteralPath $StagingPath -Destination $PublishPath
        $stagingInstalled = $true
        Assert-ManagedDirectory -Path $PublishPath -RepositoryRoot $RepositoryRoot
        Assert-PublishPayload -Path $PublishPath -RequiredFiles $RequiredFiles
    } catch {
        $swapError = $_.Exception.Message
        $rollbackError = $null
        if ($backupMoved -and (Test-Path -LiteralPath $backupPath)) {
            try {
                Restore-ManagedBackup `
                    -PublishPath $PublishPath `
                    -BackupPath $backupPath `
                    -RepositoryRoot $RepositoryRoot `
                    -PublishParent $PublishParent `
                    -PublishLeaf $PublishLeaf `
                    -BackupPrefix $BackupPrefix
            } catch {
                $rollbackError = $_.Exception.Message
            }
        } elseif ($stagingInstalled -and (Test-Path -LiteralPath $PublishPath)) {
            try {
                Remove-ManagedDirectory `
                    -Path $PublishPath `
                    -RepositoryRoot $RepositoryRoot `
                    -ExpectedParent $PublishParent `
                    -ExpectedName $PublishLeaf
            } catch {
                $rollbackError = $_.Exception.Message
            }
        }
        if ($null -ne $rollbackError) {
            if ($backupMoved) {
                throw "Publish swap failed: $swapError Rollback also failed: $rollbackError Previous output remains at $backupPath"
            }
            throw "Initial publish failed: $swapError Cleanup also failed, so the marked output was left at $PublishPath`: $rollbackError"
        }
        if ($backupMoved) {
            throw "Publish swap failed; the previous output was restored: $swapError"
        }
        if ($hadPreviousPublish) {
            throw "Publish swap failed before replacement; the existing output was left untouched: $swapError"
        }
        throw "Initial publish failed before a previous output existed: $swapError"
    }

    if ($backupMoved -and (Test-Path -LiteralPath $backupPath)) {
        try {
            Remove-ManagedDirectory `
                -Path $backupPath `
                -RepositoryRoot $RepositoryRoot `
                -ExpectedParent $PublishParent `
                -ExpectedNamePrefix $BackupPrefix
        } catch {
            Write-Warning "The new publish succeeded, but its managed backup could not be removed: $backupPath ($($_.Exception.Message))"
        }
    }
}

$repositoryRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
$frontendDirectory = Join-Path $repositoryRoot "frontend"
$originalRealModelRoot = [Environment]::GetEnvironmentVariable(
    "KSPEECH_REAL_MODEL_ROOT",
    [EnvironmentVariableTarget]::Process
)
$realModelRoot = ""
if (-not [string]::IsNullOrWhiteSpace($originalRealModelRoot)) {
    $realModelRoot = Resolve-RealModelRoot -Path $originalRealModelRoot
    Assert-RealModelLayout -Root $realModelRoot
    Write-Host "==> Real-model recognition gate enabled with $realModelRoot"
}

if ([System.IO.Path]::IsPathRooted($PublishDirectory)) {
    $publishPath = [System.IO.Path]::GetFullPath($PublishDirectory)
} else {
    $publishPath = [System.IO.Path]::GetFullPath((Join-Path $repositoryRoot $PublishDirectory))
}
$repositoryPrefix = $repositoryRoot.TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar) + [System.IO.Path]::DirectorySeparatorChar
if (-not $publishPath.StartsWith($repositoryPrefix, [System.StringComparison]::OrdinalIgnoreCase)) {
    throw "PublishDirectory must resolve inside the repository: $publishPath"
}
if ($publishPath -eq $repositoryRoot) {
    throw "Refusing to use the repository root as PublishDirectory."
}
$allowedPublishRoots = @(
    [System.IO.Path]::GetFullPath((Join-Path $repositoryRoot "publish")),
    [System.IO.Path]::GetFullPath((Join-Path $repositoryRoot "build"))
)
$publishAllowed = $false
foreach ($allowedRoot in $allowedPublishRoots) {
    $allowedPrefix = $allowedRoot.TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar) + [System.IO.Path]::DirectorySeparatorChar
    if ($publishPath -eq $allowedRoot -or $publishPath.StartsWith($allowedPrefix, [System.StringComparison]::OrdinalIgnoreCase)) {
        $publishAllowed = $true
        break
    }
}
if (-not $publishAllowed) {
    throw "PublishDirectory must be publish, build, or a child of one of those output directories: $publishPath"
}

# GetFullPath is lexical and does not reveal junction/symlink traversal. Check
# every existing component before any managed move or recursive removal.
Assert-NoReparseComponents -Path $publishPath -RepositoryRoot $repositoryRoot
$trimmedPublishPath = $publishPath.TrimEnd(
    [System.IO.Path]::DirectorySeparatorChar,
    [System.IO.Path]::AltDirectorySeparatorChar
)
$publishPath = $trimmedPublishPath
$publishParent = [System.IO.Path]::GetFullPath((Split-Path -Parent $trimmedPublishPath))
$publishLeaf = [System.IO.Path]::GetFileName($trimmedPublishPath)
if ([string]::IsNullOrWhiteSpace($publishLeaf)) {
    throw "PublishDirectory must have a concrete directory name: $publishPath"
}
$stagePrefix = ".$publishLeaf.kspeech-stage."
$backupPrefix = ".$publishLeaf.kspeech-backup."
$onnxDLLs = @("sherpa-onnx-c-api.dll", "onnxruntime.dll")
$ncnnDLLs = @(
    "kaldi-native-fbank-core.dll",
    "ncnn.dll",
    "sherpa-ncnn-c-api.dll",
    "sherpa-ncnn-core.dll"
)
$microsoftRuntimeDLLs = @(
    "MSVCP140.dll",
    "VCRUNTIME140.dll",
    "VCRUNTIME140_1.dll",
    "VCOMP140.dll"
)
$requiredDLLs = $onnxDLLs + $ncnnDLLs + $microsoftRuntimeDLLs
# Recognition extras shipped beside the executable. KSpeech points a fresh
# configuration at them on first launch, so a build without them silently loses
# hotword biasing and inverse text normalization.
$recognitionAssets = @("hotwords.txt", "itn_zh_number.fst")
$requiredPublishFiles = @("KSpeech.exe") + $requiredDLLs + $recognitionAssets
$minimumRecoverablePublishFiles = @("KSpeech.exe")
Assert-NoReparseComponents -Path $publishParent -RepositoryRoot $repositoryRoot

# Serialize repair, frontend generation, and publish swapping for this exact
# canonical output. Without the lock a second build could mistake the first
# build's active stage for stale managed output and remove it.
$mutexMaterial = [System.Text.Encoding]::UTF8.GetBytes($publishPath.ToUpperInvariant())
$mutexDigest = [Convert]::ToHexString([System.Security.Cryptography.SHA256]::HashData($mutexMaterial))
$publishMutex = [System.Threading.Mutex]::new($false, "Local\KSpeech.Build.$mutexDigest")
$publishMutexHeld = $false
# The ordinary tagged `go test ./...` executable is created in the Go cache,
# where Windows may resolve an unrelated System32 onnxruntime.dll before PATH.
# Keep the fixture variable absent until the app-local test executables run.
[Environment]::SetEnvironmentVariable(
    "KSPEECH_REAL_MODEL_ROOT",
    $null,
    [EnvironmentVariableTarget]::Process
)
try {
    try {
        $publishMutexHeld = $publishMutex.WaitOne(0)
    } catch [System.Threading.AbandonedMutexException] {
        # The previous owner exited without releasing the lock. This caller now
        # owns it and the marker-gated repair below can recover its transaction.
        $publishMutexHeld = $true
    }
    if (-not $publishMutexHeld) {
        throw "Another KSpeech build is already using PublishDirectory: $publishPath"
    }

    # Recover an interrupted same-volume rename before checking the toolchain.
    # All cleanup is marker-gated; an existing unmarked publish is never adopted.
    Repair-ManagedPublishState `
        -PublishPath $publishPath `
        -RepositoryRoot $repositoryRoot `
        -PublishParent $publishParent `
        -PublishLeaf $publishLeaf `
        -StagePrefix $stagePrefix `
        -BackupPrefix $backupPrefix `
        -RequiredFiles $minimumRecoverablePublishFiles

    $go = Get-RequiredCommand "go"
    $pnpm = Get-RequiredCommand "pnpm"
    $gcc = Get-RequiredCommand "gcc"
    $git = Get-RequiredCommand "git"

    $gccTarget = Invoke-Captured $gcc @("-dumpmachine") "Inspect MinGW target"
    if ($gccTarget -notmatch "^x86_64-w64-mingw32") {
        throw "The selected C compiler is not MinGW-w64 x64: $gccTarget"
    }

    $env:GOOS = "windows"
    $env:GOARCH = "amd64"
    $env:CGO_ENABLED = "1"
    $env:CC = $gcc

    Push-Location $repositoryRoot
    try {
    if ([string]::IsNullOrWhiteSpace($Version)) {
        $Version = Invoke-Captured $git @("describe", "--tags", "--always") "Determine build version"
    }
    if ([string]::IsNullOrWhiteSpace($Commit)) {
        $Commit = Invoke-Captured $git @("rev-parse", "--short=12", "HEAD") "Determine build commit"
    }

    Invoke-Checked $go @("mod", "download") "Download Go modules"
    Invoke-Checked $go @(
        "run", "github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.7",
        "generate", "bindings", "-clean=true", "-names", "-ts", "-i", "."
    ) "Generate Wails TypeScript bindings"

    $sherpaModuleDirectory = Invoke-Captured $go @(
        "list", "-m", "-f", "{{.Dir}}", "github.com/k2-fsa/sherpa-onnx-go-windows"
    ) "Locate sherpa-onnx Windows module"
    if ([string]::IsNullOrWhiteSpace($sherpaModuleDirectory) -or -not (Test-Path -LiteralPath $sherpaModuleDirectory -PathType Container)) {
        throw "sherpa-onnx Windows module directory was not found: $sherpaModuleDirectory"
    }

    $sherpaDLLDirectory = Join-Path $sherpaModuleDirectory "lib\x86_64-pc-windows-gnu"
    foreach ($dllName in $onnxDLLs) {
        $dllPath = Join-Path $sherpaDLLDirectory $dllName
        if (-not (Test-Path -LiteralPath $dllPath -PathType Leaf)) {
            throw "Required sherpa runtime DLL was not found: $dllPath"
        }
    }

    $sherpaNcnnModuleDirectory = Invoke-Captured $go @(
        "list", "-m", "-f", "{{.Dir}}", "github.com/k2-fsa/sherpa-ncnn-go-windows"
    ) "Locate sherpa-ncnn Windows module"
    if ([string]::IsNullOrWhiteSpace($sherpaNcnnModuleDirectory) -or
        -not (Test-Path -LiteralPath $sherpaNcnnModuleDirectory -PathType Container)) {
        throw "sherpa-ncnn Windows module directory was not found: $sherpaNcnnModuleDirectory"
    }
    $sherpaNcnnDLLDirectory = Join-Path $sherpaNcnnModuleDirectory "lib\x86_64-pc-windows-gnu"
    foreach ($dllName in $ncnnDLLs) {
        $dllPath = Join-Path $sherpaNcnnDLLDirectory $dllName
        if (-not (Test-Path -LiteralPath $dllPath -PathType Leaf)) {
            throw "Required sherpa-ncnn runtime DLL was not found: $dllPath"
        }
    }

    $microsoftRuntimeBundle = Get-MicrosoftRuntimeBundle -DLLNames $microsoftRuntimeDLLs
    foreach ($dllName in $microsoftRuntimeDLLs) {
        $runtimeRecord = $microsoftRuntimeBundle.Files[$dllName]
        Write-Host "==> Microsoft x64 runtime $dllName $($runtimeRecord.Version) <= $($runtimeRecord.Path) [$($runtimeRecord.Source)]"
    }

    # Tagged test executables load the same DLLs as the production executable.
    $env:PATH = $sherpaDLLDirectory + [System.IO.Path]::PathSeparator +
        $sherpaNcnnDLLDirectory + [System.IO.Path]::PathSeparator + $env:PATH

    Push-Location $frontendDirectory
    try {
        Invoke-Checked $pnpm @("install", "--frozen-lockfile") "Install frontend dependencies"
        Invoke-Checked $pnpm @("run", "build") "Build frontend assets"
    } finally {
        Pop-Location
    }

    Invoke-Checked $go @("test", "-count=1", "./...") "Run default Go tests"
    Invoke-Checked $go @("vet", "./...") "Run default Go vet"
    Invoke-Checked $go @("vet", "-tags", "production,sherpa,sherpancnn", "./...") "Run production native recognizer vet"

    if ([string]::IsNullOrWhiteSpace($realModelRoot)) {
        Invoke-Checked $go @("test", "-count=1", "-tags", "production,sherpa,sherpancnn", "./...") "Run production native recognizer tests"
        Write-Host "==> Skipping real-model recognition gate: KSPEECH_REAL_MODEL_ROOT is unset; no models are downloaded."
    } else {
        Write-Host "==> Skipping ordinary production tagged go test: app-local real-model executables supersede it and prevent System32 DLL precedence."
        Invoke-RealModelRecognitionGate `
            -Go $go `
            -ModelRoot $realModelRoot `
            -SherpaDLLDirectory $sherpaDLLDirectory `
            -OnnxDLLs $onnxDLLs `
            -SherpaNcnnDLLDirectory $sherpaNcnnDLLDirectory `
            -NcnnDLLs $ncnnDLLs `
            -MicrosoftRuntimeBundle $microsoftRuntimeBundle
    }

    if (-not (Test-Path -LiteralPath $publishParent -PathType Container)) {
        [System.IO.Directory]::CreateDirectory($publishParent) | Out-Null
    }
    Assert-NoReparseComponents -Path $publishParent -RepositoryRoot $repositoryRoot
    $stagingPath = New-TransactionPath -Parent $publishParent -Prefix $stagePrefix
    New-ManagedDirectory `
        -Path $stagingPath `
        -RepositoryRoot $repositoryRoot `
        -ExpectedParent $publishParent `
        -ExpectedNamePrefix $stagePrefix

    try {
        $executablePath = Join-Path $stagingPath "KSpeech.exe"
        $linkerFlags = "-s -w -H=windowsgui -X main.version=$Version -X main.commit=$Commit"
        Invoke-Checked $go @(
            "build",
            "-trimpath",
            "-tags", "production,sherpa,sherpancnn",
            "-ldflags", $linkerFlags,
            "-o", $executablePath,
            "."
        ) "Build production Windows x64 executable"

        foreach ($dllName in $onnxDLLs) {
            Copy-Item `
                -LiteralPath (Join-Path $sherpaDLLDirectory $dllName) `
                -Destination (Join-Path $stagingPath $dllName) `
                -Force
        }
        foreach ($dllName in $ncnnDLLs) {
            Copy-Item `
                -LiteralPath (Join-Path $sherpaNcnnDLLDirectory $dllName) `
                -Destination (Join-Path $stagingPath $dllName) `
                -Force
        }
        Copy-MicrosoftRuntimeBundle `
            -Bundle $microsoftRuntimeBundle `
            -DestinationDirectory $stagingPath
        foreach ($assetName in $recognitionAssets) {
            $assetPath = Join-Path $repositoryRoot "assets\$assetName"
            if (-not (Test-Path -LiteralPath $assetPath -PathType Leaf)) {
                throw "Required recognition asset was not found: $assetPath"
            }
            Copy-Item -LiteralPath $assetPath -Destination (Join-Path $stagingPath $assetName) -Force
        }
        if (Test-Path -LiteralPath (Join-Path $repositoryRoot "LICENSE") -PathType Leaf) {
            Copy-Item `
                -LiteralPath (Join-Path $repositoryRoot "LICENSE") `
                -Destination (Join-Path $stagingPath "LICENSE.txt") `
                -Force
        }

        Assert-PublishPayload -Path $stagingPath -RequiredFiles $requiredPublishFiles
        Invoke-PublishSwap `
            -StagingPath $stagingPath `
            -PublishPath $publishPath `
            -RepositoryRoot $repositoryRoot `
            -PublishParent $publishParent `
            -PublishLeaf $publishLeaf `
            -StagePrefix $stagePrefix `
            -BackupPrefix $backupPrefix `
            -RequiredFiles $requiredPublishFiles `
            -PreviousRequiredFiles $minimumRecoverablePublishFiles
    } finally {
        if (Test-Path -LiteralPath $stagingPath) {
            try {
                Remove-ManagedDirectory `
                    -Path $stagingPath `
                    -RepositoryRoot $repositoryRoot `
                    -ExpectedParent $publishParent `
                    -ExpectedNamePrefix $stagePrefix
            } catch {
                Write-Warning "Leaving failed staging path untouched because safe cleanup could not be verified: $stagingPath ($($_.Exception.Message))"
            }
        }
    }

    Write-Host "==> Published KSpeech $Version ($Commit) to $publishPath"
    Get-ChildItem -LiteralPath $publishPath -File | Sort-Object Name | Format-Table Name, Length
    } finally {
        Pop-Location
    }
} finally {
    if ($publishMutexHeld) {
        $publishMutex.ReleaseMutex()
    }
    $publishMutex.Dispose()
    [Environment]::SetEnvironmentVariable(
        "KSPEECH_REAL_MODEL_ROOT",
        $originalRealModelRoot,
        [EnvironmentVariableTarget]::Process
    )
}
