# 开发文档

当前开发入口是仓库根目录的 Go 模块（`github.com/kangzyz/KSpeech`）与 `frontend/`。旧的 .NET 源码树已删除，仓库不再包含 C# 实现。

## 仓库结构

| 路径 | 内容 |
| --- | --- |
| `main.go` | Wails 应用入口：主窗口与设置窗口、托盘、事件注册 |
| `desktop_service.go` | 前端唯一的服务边界：快照、配置写入、资源操作 |
| `internal/` | 配置、任务调度、AI 助手、插件注册表、音频源、识别引擎、资源管理 |
| `frontend/src/` | Vue 3 界面：`views/` 主窗口与设置窗口、`components/` 三栏面板与通用控件、`styles.css` 设计令牌 |
| `frontend/bindings/` | `wails3 generate bindings` 生成的 TypeScript 绑定，不要手工维护 |
| `scripts/build-windows.ps1` | Windows x64 生产构建与原子发布 |
| `scripts/build-installer.ps1` | 校验发布目录并编译 Windows 安装程序 |
| `installer/` | Inno Setup 脚本、随仓库分发的简体中文语言文件、向导配图 BMP |
| `scripts/make-brand-assets.py` | 从 `imgs/` 重新导出图标、品牌标记、向导配图与 README 横幅 |
| `scripts/check-hotwords.py` | 按模型的 tokens.txt 与 bpe.vocab 校验 `assets/hotwords.txt`，改词表后必跑 |

## 本地验证

```powershell
pnpm --dir .\frontend install --frozen-lockfile
pnpm --dir .\frontend run build
go test .\... -count=1
go vet .\...
go build -o .\build-dev\KSpeech.exe .
```

普通 `go build` 使用无原生推理的 Sherpa stub，适合 UI、配置和外部命令识别器开发。

调试界面时可以设置 `KSPEECH_WEBVIEW_DEBUG_PORT`，WebView2 会开启远程调试端口，从而可以直接检查两个窗口的 DOM 与控制台：

```powershell
$env:KSPEECH_WEBVIEW_DEBUG_PORT = "9333"; .\build-dev\KSpeech.exe
```

## 生产构建

```powershell
.\scripts\build-windows.ps1 -Version 0.1.0 -Commit (git rev-parse --short HEAD)
```

生产构建要求 `CGO_ENABLED=1` 和可用的 MinGW GCC。脚本会重新生成 Wails 绑定，将 sherpa-onnx、sherpa-ncnn Windows 模块的 DLL 与其所需的 Microsoft VC++/OpenMP x64 运行库放入 `publish/`（产物为 `KSpeech.exe`）；Microsoft 运行库优先取自本机完整 Visual Studio Redist，找不到时才检查 System32，缺失或架构错误会中止。

两个脚本都要用 PowerShell 7 (`pwsh`) 跑：里面用到的 `[System.IO.Path]::GetRelativePath` 在 Windows PowerShell 5.1 上不存在，会在校验发布目录时直接报 `MethodNotFound`。MinGW 也必须在 `PATH` 里（本机在 `C:\ProgramData\mingw64\mingw64\bin`）。

发布目录通过 `.kspeech-build-output` 标记文件识别，只有带该标记的目录才会被复用或回滚；改名之前留下的 `.tmspeech-build-output` 已经不算数，脚本会拒绝接管这样的目录，把旧 `publish/` 挪走重建即可。构建完成后仍必须在干净的 Windows 11 x64 虚拟机验证 WebView2、系统/麦克风回环、托盘、主窗口的透明与边缘缩放、ONNX/NCNN 模型加载和退出清理。

## 真实模型门禁

如本机已有真实测试夹具，可在构建前设置 `KSPEECH_REAL_MODEL_ROOT`。目录必须包含 `test.wav`、`onnx/{encoder.onnx,decoder.onnx,joiner.onnx,tokens.txt}` 和 `ncnn/{encoder.param,encoder.bin,decoder.param,decoder.bin,joiner.param,joiner.bin,tokens.txt}`；脚本会为两个后端分别编译测试 EXE，把匹配 DLL 放在 EXE 同目录后执行真实识别门禁。环境变量未设置时脚本会明确跳过，且不会自动下载模型。

## 安装包

```powershell
.\scripts\build-installer.ps1 -Version 0.1.0
```

脚本按 `build-windows.ps1` 的同一份文件清单校验 `publish/`（`-PayloadDirectory` 可指向其他构建输出），再用 Inno Setup 6.3+ 的 `ISCC.exe` 编译 `installer/kspeech.iss`，产物是 `build/installer/KSpeech-<版本>-win-x64-setup.exe`，最后打印体积与 SHA-256。省略 `-Version` 时取 `git describe --tags --always`；Inno 只接受纯数字版本号，所以 `VersionInfoVersion` 只截版本号开头的数字部分。ISCC 依次从 `-IsccPath`、`KSPEECH_ISCC`、PATH、卸载注册表项和 Program Files 查找。

安装程序的行为：

- 默认按用户安装到 `%LOCALAPPDATA%\Programs\KSpeech`，不触发 UAC；提权运行或加 `/ALLUSERS` 才装进 Program Files。
- 界面语言为简体中文（`installer/languages/ChineseSimplified.isl`，Inno 官方发行版不带，故随仓库分发）与英文，按系统语言自动选择。
- 安装前检查 `HKLM32/HKLM64/HKCU` 下 EdgeUpdate 的 WebView2 客户端键，缺失时静默下载并运行官方引导程序；下载或安装失败只提示，不中断安装。判定结果会写进安装日志。
- 安装与卸载都通过重启管理器关闭正在运行的 KSpeech，避免 DLL 被占用。
- 卸载时询问是否一并删除 `%APPDATA%\KSpeech` 与`我的文档\KSpeechLogs`，默认保留；静默卸载一律保留。
- 要求 x64 与 Windows 10 2004（10.0.19041）及以上，和进程回环采集的最低要求一致。

静默安装与卸载：

```powershell
.\build\installer\KSpeech-0.1.0-win-x64-setup.exe /VERYSILENT /SUPPRESSMSGBOXES /NORESTART
& "$env:LOCALAPPDATA\Programs\KSpeech\unins000.exe" /VERYSILENT /SUPPRESSMSGBOXES
```

改动 `installer/kspeech.iss` 后注意保留文件的 UTF-8 BOM，否则 Inno 会把其中的中文按 ANSI 读取。

## 图标与品牌资源

`imgs/` 是设计原图，按内容命名；仓库里真正被程序和安装包读取的是从它们导出的生成物。改图之后跑一次

```powershell
python .\scripts\make-brand-assets.py   # 需要 Pillow 与 NumPy
```

就能把下面这些全部重新导出：

| 生成物 | 来源 | 用在哪 |
| --- | --- | --- |
| `assets/kspeech-circle.ico` | `imgs/icon-card-dark.png` | 安装包 `SetupIconFile`，以及生成 `rsrc_windows_amd64.syso` 的输入 |
| `assets/kspeech-app.png` | `imgs/icon-card-dark.png` | `main.go` 传给 Wails 的 `Options.Icon`（窗口图标的运行时兜底） |
| `assets/kspeech-tray.png` | `imgs/icon-card-dark-plain.png` | 托盘图标；没有取景框，16 px 下最清楚 |
| `assets/kspeech-tray.ico` | `imgs/icon-card-dark-plain.png` | 托盘图标的 ico 版本，留给需要 ico 的场合 |
| `frontend/src/assets/brand-mark.png` | `imgs/icon-card-dark.png` | 设置窗侧栏与「关于」的品牌标记，深浅两个主题共用 |
| `installer/wizard-image.bmp` | `imgs/installer-banner.png` | 安装向导欢迎页/完成页的竖幅 |
| `installer/wizard-small-image.bmp` | `imgs/icon-card-dark.png` | 安装向导页眉右上角的小图 |
| `imgs/banner.png` | `imgs/banner-source.png` | README 顶部横幅，1600×900、256 色 |
| `imgs/social-preview.png` | `imgs/social-preview-source.png` | GitHub 仓库的社交预览图，1280×640、256 色；仓库本身不引用，要在 GitHub 的 Settings → General → Social preview 手动上传 |
| `rsrc_windows_amd64.syso` | `assets/kspeech-circle.ico` | 链接进 EXE 的 Windows 资源：文件图标（资源管理器、桌面快捷方式）与版本信息，**不由脚本生成**，见下 |

约定与坑：

- `.ico` 含 16/20/24/32/40/48/64/128/256 九档。用深蓝卡片而不是白卡片：它在浅色资源管理器、深色任务栏和界面深浅两个主题上都能读，白卡片在浅色背景上会糊成一团。
- 图标原图必须带 alpha、不能自带投影，脚本会直接拒绝没有 alpha 通道的图标源——白底原图缩到 16 px 时抠出来的边缘会留下毛边。
- 三张海报（横幅、竖幅、社交预览）都是位图原图，导出时统一转 256 色调色板：这类扁平矢量风格的图几乎看不出差别，体积只有真彩 PNG 的三分之一。竖幅的比例必须保持 164:314，那是 Inno 向导图的固定比例。
- README 引用的界面截图按内容命名：`imgs/console.png`（主窗口三栏）、`console-compact.png`（只留字幕的紧凑形态）、`settings-general.png`、`settings-appearance.png`、`settings-recognizer.png`、`settings-resources.png`。重拍时保持文件名不变即可，不需要改 README。
- 重拍主窗口时**不要直接对着桌面截屏**：那个窗口是透明的，会把你桌面上的内容一起拍进仓库。做法是先用一个无边框置顶的 WinForms 窗口铺一张生成的渐变图当背景，把主窗口叠上去，再按 `DwmGetWindowAttribute(DWMWA_EXTENDED_FRAME_BOUNDS)` 取到的矩形 `CopyFromScreen`（`GetWindowRect` 会多出不可见的调整边框，把背景外的像素也框进来）。截图里的会议内容用 `window._wails.dispatchWailsEvent` 灌一份假快照即可，不必真的开麦。设置窗口是不透明的，直接截窗口矩形就行，四角按 12px 圆角抠成透明。
- 向导图必须是 BMP：Inno Setup 到 6.5.2 才支持 PNG，而构建脚本只要求 6.3+。竖幅按 200% DPI（328×628）导出，让向导自己缩小而不是放大。
- **Wails 只吃 PNG，不吃 .ico**：`Options.Icon` 和托盘图标最终都走 `CreateIconFromResourceEx`，它接受 PNG 或单张图标资源，但不认整份 .ico 文件头。以前传 .ico 的结果是托盘启动时报 `failed to create systray icon`，窗口图标则回落到系统默认。所以 `main.go` 嵌入的是 PNG。
- **窗口图标真正的来源是资源 ID 3**：Wails 建窗口时先 `LoadIconWithResourceID(module, 3)`，失败才用 `Options.Icon`。`.syso` 因此必须把图标登记成 **ID 3**（`wails3 generate syso` 也是这个约定），否则标题栏、Alt+Tab、任务栏都是空白图标。托盘在自带图标失败时也会回落到这个资源。
- `.syso` 用 `github.com/tc-hib/winres` 在仓库外的临时模块里生成（主模块因此不引入这个依赖），入参是 `assets/kspeech-circle.ico`，图标标识用 `winres.ID(3)`，里面的 `FileVersion`/`ProductVersion` 是写死的 `0.1.0.0`，发布新版本时要重新生成一次。删掉这个文件不影响构建，只会让 EXE 退回没有图标和版本信息的状态。
- 换过图标后 Windows 会缓存旧图标，桌面快捷方式和资源管理器可能要重开 `explorer.exe` 才刷新。

## 界面约定

- `frontend/src/styles.css` 是唯一的设计令牌来源：深色为默认，浅色通过 `prefers-color-scheme` 覆盖。新样式一律消费 `var(--*)`，不要写死颜色。
- 主窗口必须保持透明：`html[data-view="console"]` 不设背景，毛玻璃是 `.console` 自己的 `--overlay-bg` + `backdrop-filter`。用户配的背景色和悬停色叠在 `.console::before` 上，直接写成 `.console` 的 `background` 会把毛玻璃盖掉。
- 只有标题栏 `.console-bar` 是拖动区（`--wails-draggable: drag`，锁定时前端换成 `no-drag`），栏内按钮和分隔条都写死 `no-drag`；三栏正文要能选中文字，绝不能整窗设成拖动区。
- 无边框窗口的边缘缩放由 `@wailsio/runtime` 在前端做：它监听窗口最外侧约 5px 并发 `wails:resize:*`，且只在 `setResizable(true)` 之后生效。所以 `.console-body` 留 3px padding 让开边缘，`SetLocked(true)` 关掉 resizable 就等于关掉缩放。
- 字幕样式（字体、颜色、阴影、对齐）由 `captionStyle.ts` 统一计算，消息流与设置页的实时预览共用 `messageStyleFrom`；`appearance.FontSize` 按整屏字幕的老语义存，进消息流前由 `messageFontSize` 缩放。
- 字幕颜色有对比度兜底：出厂的白字黑描边是给旧版全透明字幕窗的，在浅色毛玻璃面板上会白底白字，所以 `captionInk` 按 WCAG 对比度判断，低于 3:1 就让位给主题色并丢掉阴影。改这块时两个主题都要看一眼——同一个白色在深色主题下是正确的。
- 前端只能通过 `frontend/src/api.ts` 访问后端；新增后端方法后需要重新生成绑定。
- 界面改动可以不靠肉眼：用 `KSPEECH_WEBVIEW_DEBUG_PORT` 起应用后，`http://127.0.0.1:<端口>/json/list` 列出两个窗口，接上 `webSocketDebuggerUrl` 用 CDP 的 `Runtime.evaluate` 就能查 DOM、点按钮，`window._wails.dispatchWailsEvent({name:"kspeech:state",data:{…}})` 还能灌一份假快照来看没有模型时的渲染效果。`python scripts/inspect-console.py <端口> <截图.png>` 是这套流程的现成脚本：灌一条带长链接的假回答，量一遍是否溢出并抓图。连 DevTools 的 WebSocket 必须 `suppress_origin=True`，否则 WebView2 直接 403。测完记得 `taskkill /IM KSpeech.exe /F`；调试实例最好用临时 `APPDATA`，免得写脏自己的配置。

## 调试 AI 助手

`internal/assistant` 的所有测试都用假的 `Completer` 与假时钟，不会真的联网。要在界面里验证整条链路，起一个本地的 OpenAI 兼容服务（Ollama、LM Studio，或临时写一个只返回固定 JSON 的 HTTP 服务），在「设置 → AI 助手」里把地址填成 `http://127.0.0.1:<端口>/v1` 即可——`http` 只对本机和内网地址放行。想避开自己的真实配置，可以在启动时覆盖 `APPDATA` 指向一个临时目录，KSpeech 会在那里新建 `KSpeech\config.json`。

## 外部命令的相对批处理路径

`cmd.exe` 只在 PATH 里查找裸命令名，仅当 `NoDefaultCurrentDirectoryInExePath` 未设置时才回退到当前目录，而 Windows 自己会在若干环境里设上它（Git for Windows 的 shell 就是其中之一）。因此 `process_windows.go` 会给相对的 `.bat`/`.cmd` 补一个显式 `.\` 前缀，交给 cmd.exe 的路径始终相对——工作目录是在 `configureProcess` 之后才赋给 `cmd.Dir` 的。绝对路径与已经带 `.\`、`..\` 的路径原样传递。

架构、兼容策略和未完成验收项见 [docs/go-rewrite.md](docs/go-rewrite.md)。
