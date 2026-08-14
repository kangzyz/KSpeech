; KSpeech Windows x64 installer definition.
;
; Compile through scripts\build-installer.ps1: it validates the payload against
; the same file manifest scripts\build-windows.ps1 publishes, resolves the
; version and commit, and supplies every define below. Compiling this script
; directly with ISCC falls back to the repository's publish\ output.
;
; Requires Inno Setup 6.3 or newer (ArchitecturesAllowed=x64compatible).

#ifndef PayloadDir
  #define PayloadDir SourcePath + "..\publish"
#endif
#ifndef OutputDir
  #define OutputDir SourcePath + "..\build\installer"
#endif
#ifndef AppVersion
  #define AppVersion "0.0.0-dev"
#endif
#ifndef VersionInfoVersion
  #define VersionInfoVersion "0.0.0.0"
#endif
#ifndef AppCommit
  #define AppCommit ""
#endif
#ifndef OutputBaseName
  #define OutputBaseName "KSpeech-" + AppVersion + "-win-x64-setup"
#endif

#define AppName "KSpeech"
#define AppExeName "KSpeech.exe"
#define AppPublisher "jxlpzqc, am009"
#define AppURL "https://github.com/kangzyz/KSpeech"
; Build markers identify a managed build output; they are never installed.
#define BuildMarker ".kspeech-build-output"

#if !FileExists(AddBackslash(PayloadDir) + AppExeName)
  #error Payload is incomplete: KSpeech.exe was not found. Run scripts\build-windows.ps1 first.
#endif

[Setup]
; Never reuse this GUID for another application; upgrades and uninstall
; entries are keyed on it.
AppId={{7E4B2F5C-9A1D-4C36-8E70-2B5A9D6F1C48}
AppName={#AppName}
AppVersion={#AppVersion}
AppVerName={#AppName} {#AppVersion}
AppPublisher={#AppPublisher}
AppPublisherURL={#AppURL}
AppSupportURL={#AppURL}
AppUpdatesURL={#AppURL}
VersionInfoVersion={#VersionInfoVersion}
VersionInfoProductVersion={#VersionInfoVersion}
VersionInfoProductTextVersion={#AppVersion}
#if AppCommit != ""
VersionInfoDescription={#AppName} {#AppVersion} setup ({#AppCommit})
#else
VersionInfoDescription={#AppName} {#AppVersion} setup
#endif
DefaultDirName={autopf}\{#AppName}
DefaultGroupName={#AppName}
DisableProgramGroupPage=yes
AllowNoIcons=yes
UninstallDisplayName={#AppName} {#AppVersion}
UninstallDisplayIcon={app}\{#AppExeName}
LicenseFile={#SourcePath}..\LICENSE
SetupIconFile={#SourcePath}..\assets\kspeech-circle.ico
; Wizard artwork must stay BMP: Inno only started reading PNG here in 6.5.2,
; while the build script accepts 6.3 and newer. Both files are generated from
; imgs/ by scripts/make-brand-assets.py, at 200% scale for the wizard to shrink.
WizardImageFile={#SourcePath}wizard-image.bmp
WizardSmallImageFile={#SourcePath}wizard-small-image.bmp
OutputDir={#OutputDir}
OutputBaseFilename={#OutputBaseName}
Compression=lzma2/max
SolidCompression=yes
WizardStyle=modern
; Per-user by default so no elevation is needed; an elevated run or
; /ALLUSERS installs for every account instead.
PrivilegesRequired=lowest
PrivilegesRequiredOverridesAllowed=dialog commandline
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
; Process loopback capture needs Windows 10 2004 or newer.
MinVersion=10.0.19041
; Restart Manager closes a running KSpeech instead of failing on locked DLLs.
CloseApplications=yes
CloseApplicationsFilter=*.exe,*.dll
RestartApplications=no
SetupMutex={#AppName}Setup,Global\{#AppName}Setup
ShowLanguageDialog=auto

[Languages]
Name: "chinesesimplified"; MessagesFile: "{#SourcePath}languages\ChineseSimplified.isl"
Name: "english"; MessagesFile: "compiler:Default.isl"

[CustomMessages]
chinesesimplified.WebView2Required=KSpeech 需要 Microsoft Edge WebView2 运行时才能显示界面。自动安装未成功，安装会继续，但请先手动安装 WebView2 运行时再启动 KSpeech。
chinesesimplified.WebView2DownloadFailed=下载 WebView2 运行时安装程序失败。
chinesesimplified.WebView2InstallFailed=无法启动 WebView2 运行时安装程序。
chinesesimplified.WebView2InstallExitCode=WebView2 运行时安装程序返回错误码 %1。
chinesesimplified.RemoveUserData=是否同时删除 KSpeech 的配置、已安装模型和日志？%n%n配置与模型：%%APPDATA%%\KSpeech%n日志：我的文档\KSpeechLogs
english.WebView2Required=KSpeech needs the Microsoft Edge WebView2 runtime to render its interface. Installing it automatically did not succeed; setup will continue, but install the WebView2 runtime manually before starting KSpeech.
english.WebView2DownloadFailed=Downloading the WebView2 runtime installer failed.
english.WebView2InstallFailed=The WebView2 runtime installer could not be started.
english.WebView2InstallExitCode=The WebView2 runtime installer returned exit code %1.
english.RemoveUserData=Also delete KSpeech configuration, installed models and logs?%n%nConfiguration and models: %%APPDATA%%\KSpeech%nLogs: Documents\KSpeechLogs

[Tasks]
Name: "desktopicon"; Description: "{cm:CreateDesktopIcon}"; GroupDescription: "{cm:AdditionalIcons}"; Flags: unchecked

[Files]
Source: "{#PayloadDir}\*"; DestDir: "{app}"; Excludes: "{#BuildMarker}"; Flags: ignoreversion recursesubdirs createallsubdirs

[Icons]
Name: "{autoprograms}\{#AppName}"; Filename: "{app}\{#AppExeName}"
Name: "{autodesktop}\{#AppName}"; Filename: "{app}\{#AppExeName}"; Tasks: desktopicon

[Run]
Filename: "{app}\{#AppExeName}"; Description: "{cm:LaunchProgram,{#StringChange(AppName, '&', '&&')}}"; Flags: nowait postinstall skipifsilent

[Code]
const
  WebView2ClientPath =
    'SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}';
  WebView2BootstrapperUrl = 'https://go.microsoft.com/fwlink/p/?LinkId=2124703';
  WebView2BootstrapperName = 'MicrosoftEdgeWebview2Setup.exe';

var
  WebView2Checked: Boolean;

function HasWebView2Version(const RootKey: Integer): Boolean;
var
  Version: String;
begin
  Result := RegQueryStringValue(RootKey, WebView2ClientPath, 'pv', Version) and
    (Version <> '') and (Version <> '0.0.0.0');
end;

function HasWebView2Runtime: Boolean;
begin
  // Per-machine runtimes register under the 32-bit view even on x64; per-user
  // ones only appear under HKCU.
  Result := HasWebView2Version(HKLM32) or HasWebView2Version(HKLM64) or
    HasWebView2Version(HKCU);
end;

function EnsureWebView2Runtime(var ErrorMessage: String): Boolean;
var
  ResultCode: Integer;
begin
  Result := True;
  ErrorMessage := '';
  if HasWebView2Runtime then
  begin
    Log('WebView2 runtime is already installed; skipping the bootstrapper.');
    Exit;
  end;

  Log('WebView2 runtime was not found; downloading ' + WebView2BootstrapperUrl);
  try
    DownloadTemporaryFile(WebView2BootstrapperUrl, WebView2BootstrapperName, '', nil);
  except
    ErrorMessage := CustomMessage('WebView2DownloadFailed') + #13#10 + GetExceptionMessage;
    Result := False;
    Exit;
  end;

  // Without elevation the bootstrapper installs the runtime for the current
  // user, which is what a per-user KSpeech install needs.
  if not Exec(ExpandConstant('{tmp}\' + WebView2BootstrapperName), '/silent /install',
    '', SW_HIDE, ewWaitUntilTerminated, ResultCode) then
  begin
    ErrorMessage := CustomMessage('WebView2InstallFailed');
    Result := False;
    Exit;
  end;

  Log('WebView2 bootstrapper exit code: ' + IntToStr(ResultCode));
  if ResultCode <> 0 then
  begin
    ErrorMessage := FmtMessage(CustomMessage('WebView2InstallExitCode'), [IntToStr(ResultCode)]);
    Result := False;
  end;
end;

function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  ErrorMessage: String;
begin
  // A missing runtime is reported but never blocks the install: the payload is
  // still usable once WebView2 is installed by other means.
  Result := '';
  NeedsRestart := False;
  if WebView2Checked then
    Exit;
  WebView2Checked := True;
  if not EnsureWebView2Runtime(ErrorMessage) then
    SuppressibleMsgBox(CustomMessage('WebView2Required') + #13#10#13#10 + ErrorMessage,
      mbError, MB_OK, IDOK);
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var
  DataDir: String;
  LogDir: String;
begin
  if CurUninstallStep <> usPostUninstall then
    Exit;

  DataDir := ExpandConstant('{userappdata}\{#AppName}');
  LogDir := ExpandConstant('{userdocs}\{#AppName}Logs');
  if not (DirExists(DataDir) or DirExists(LogDir)) then
    Exit;

  // Silent uninstalls take the default answer and keep user data.
  if SuppressibleMsgBox(CustomMessage('RemoveUserData'), mbConfirmation,
    MB_YESNO or MB_DEFBUTTON2, IDNO) = IDYES then
  begin
    Log('Removing user data: ' + DataDir + ' and ' + LogDir);
    DelTree(DataDir, True, True, True);
    DelTree(LogDir, True, True, True);
  end
  else
    Log('Keeping user data in ' + DataDir + ' and ' + LogDir);
end;
