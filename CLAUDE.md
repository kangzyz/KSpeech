# KSpeech 架构文档

KSpeech 是一个 Windows 本地实时语音字幕应用：捕获系统播放的声音或麦克风，实时转成文字，浮在屏幕上显示。

**当前实现是仓库根目录的 Go 模块 `github.com/kangzyz/KSpeech` 加 `frontend/` 的 Vue 3 界面。** 旧的 .NET/Avalonia 实现不在本仓库里，需要对照历史行为时看上游 [TMSpeech](https://github.com/jxlpzqc/TMSpeech) 的 `src/`。

注意，尽量不要搜索代码文件，而是找到相关的文件并阅读整个文件。

## 当前架构（Go + Wails 3 + Vue 3）

```
KSpeech/
├── main.go                 # Wails 入口：主窗口 + 设置窗口、托盘、事件注册
├── desktop_service.go      # 前端唯一服务边界：快照、配置写入、资源操作
├── internal/
│   ├── config/             # 扁平配置 Store、默认值、插件键转义、多路音频通道解析
│   ├── job/                # 多路音频源与各自识别器的生命周期编排、历史、日志、通知
│   ├── assistant/          # 可选 AI 助手：OpenAI 兼容客户端、要点汇总、问答、服务商内置工具目录
│   ├── punctuation/        # 整句定稿后的标点还原：规则实现 + 可选 CT-Transformer 模型
│   ├── plugin/             # 插件契约与并发安全注册表
│   ├── audio/              # WASAPI 麦克风 / 全局回环 / 进程回环
│   ├── recognizer/         # sherpaonnx、sherpancnn、command 三个识别引擎
│   ├── resource/           # 模型市场、下载安装、事务与回滚
│   └── processlist/        # 进程枚举（指定进程回环用）
├── frontend/
│   ├── src/views/          # ConsoleView（三栏主窗口）/ SettingsView
│   ├── src/components/     # MessageStream、InsightList、AssistantChat 三栏，加 AppIcon、ToggleSwitch 等通用控件
│   ├── src/styles.css      # 设计令牌（深色默认 + prefers-color-scheme 浅色）
│   ├── src/captionStyle.ts # ARGB 配置 → CSS 字幕样式（消息流与设置预览共用）
│   ├── src/api.ts          # 前端访问后端的唯一入口
│   └── bindings/           # wails3 生成的 TS 绑定，勿手工维护
├── assets/                 # 应用与托盘图标：运行时用 *.png（Wails 只认 PNG），安装包与 syso 用 *.ico
├── rsrc_windows_amd64.syso # EXE 内嵌的 Windows 图标与版本信息资源
├── imgs/                   # 品牌原图与截图（按内容命名：icon-card-*.png、installer-banner.png、banner*.png）
├── installer/              # Inno Setup 脚本、语言文件、向导 BMP
└── scripts/build-windows.ps1
```

关键约定：

- **数据目录**：`%APPDATA%\KSpeech`（配置 + 已安装资源），默认日志目录 `我的文档\KSpeechLogs`。
- **窗口**：只有两个。主窗口（`consoleWindowName`，`/?view=console`）是无边框透明置顶的三栏面板——左实时字幕+历史、中要点、右 AI 对话，默认 940×420，位置与大小存在 `general.MainWindowLocation`；设置是普通窗口。旧的字幕/历史/助手三窗口已删除。**锁定 = 固定位置与大小**（`SetResizable(false)` + 前端把标题栏的 `--wails-draggable` 换成 `no-drag`），不再是鼠标穿透，所以锁定后照样能滚字幕、能提问；隐藏走 `HideConsole()`，只能从托盘图标恢复，所以托盘必须先于任何隐藏路径建好。
- **无边框缩放**：Wails 的窗口边缘缩放是**前端实现**的——`@wailsio/runtime` 监听窗口最外侧约 5px，命中就发 `wails:resize:*`；它只在 `setResizable(true)` 之后才武装。所以 `.console-body` 留了 3px padding 把内容让开窗口边缘，锁定时后端关掉 resizable 就等于关掉边缘缩放。
- **事件**：`kspeech:state`（完整快照）、`kspeech:live`（30 Hz 合并的字幕与计时，含每路音频各自的实时字幕）、`kspeech:notification`、`kspeech:assistant`（AI 助手要点与问答）。
- **多路音频**：`audio.channels` 是一个 JSON 数组字符串（`config.Channel` 的 source + label），空值回退到旧的单路 `audio.source` 且不带说话人标签。每一路在运行时都会拿到**自己的识别器实例**——第一路复用注册表里的那个，其余由 `job.RecognizerFactory` 现建现关，所以多录一路就是多一份模型内存和 CPU。历史条目带 `Speaker`/`Channel`，说话人颜色由前端按 `audioSources` 的顺序分配。
- **网络边界**：除资源下载外，只有开启后的 AI 助手会外发数据，且只发已完成的整句字幕；`internal/assistant` 是唯一允许调用用户模型接口的地方。助手声明的内置工具（联网搜索）由服务商在自己那边执行，KSpeech 不直接访问搜索接口。
- **代理**：资源下载的默认 transport 用 `internal/resource/proxy.go` 的 `ProxyFromEnvironmentOrSystem`——环境变量优先，没有才回退读 Windows 系统代理（`proxy_windows.go` 读 WinINET 注册表）。Go 自带的 `ProxyFromEnvironment` **不读注册表**，而 GUI 启动的进程通常没有代理环境变量，所以少了这层用户开着代理也连不上 GitHub。两个来源在进程内都只读一次（Go 自己缓存环境变量），因此测试要走可注入的 `resolveProxy` 而不是改进程环境。
- **插件键**：`config.PluginKey(moduleID, pluginID)`，模块 ID 前缀已改为 `KSpeech.*`，转义规则沿用旧 `PluginManager.GetFullKey`。
- **构建标签**：默认构建使用 Sherpa stub；生产构建使用 `production,sherpa,sherpancnn` 并需要 CGO + MinGW。
- **热词编码**：sherpa-onnx 用模型的 modeling unit 编码热词，C API 默认 `cjkchar`，会把英文整词当成一个 token 查表从而必然落空。`internal/recognizer/sherpaonnx/config.go` 的 `resolveHotwordTokenization` 在配置了热词时自动探测 tokens.txt 旁边的 `bpe.vocab`：有就切 `cjkchar+bpe`，没有就留在 `cjkchar`。声明 bpe 类 modeling unit 却缺 `bpe.vocab` 会让原生库空指针崩溃，所以校验层直接拒绝。**热词只在 `modified_beam_search` 下生效，但配置里允许两者并存**：首次运行种下的内置词表让每份配置都带 `HotwordsFile`，若校验层拒绝这个组合，设置页的解码方式下拉框就永远换不到 `greedy_search`（只会提示保存失败）。所以路径一直留在配置里，由 `prepareConfig` 在 greedy 时丢掉，再由 `desktop_service.go` 的 `hotwordsHint` 在热词输入框下说明当前不生效。
- **样式**：一切颜色走 `styles.css` 的 `var(--*)` 令牌；主窗口的 `html`/`body` 必须保持透明背景，毛玻璃面板画在 `.console` 上，`appearance.BackgroundColor` / `MouseHover` 由 `.console::before` 叠色而不是当背景，否则会盖掉毛玻璃。
- **字号**：`appearance.FontSize` 仍按老的整屏字幕语义存（默认 48），消息流里由 `captionStyle.ts` 的 `messageFontSize` 按 0.42 缩放并夹在 12–34px。改缩放就要同时改设置页那句提示，预览组件和消息流共用同一个函数。
- **字幕颜色的对比度兜底**：`appearance.FontColor` 的出厂值是纯白 + 黑描边——那是给旧版**全透明**字幕窗设计的，放到浅色主题的毛玻璃面板上就是白底白字，而每一份已有配置里都存着这个值。所以 `captionStyle.ts` 的 `captionInk` 会先算配置色与面板的 WCAG 对比度（面板亮度按主题取 `PANEL_LUMINANCE` 常量，alpha 先合成进去），低于 3:1 就整个让位：不写 `color`，由 `.console` 的 `--overlay-text` 继承，阴影也一并丢掉（阴影和白字是同一套遗留组合）。**不要改成迁移配置值**：同一个白色在深色主题下是对的，只有浅色主题该退让，按主题实时判断才两边都对。设置页用 `captionColourApplies` 在颜色卡片下说明这次为什么没生效，主题切换的响应性来自 `theme.ts` 的 `prefersDark`。

### 常见改动落点

| 改动 | 落点 |
| --- | --- |
| 新增配置项 | `internal/config/keys.go` 定义键 → `internal/config/defaults.go` 给默认值 → `desktop_service.go` 的 `editableConfigKeys` 与 `normalizeEditableConfigValue` 放行并校验 → `SettingsView.vue` 加控件 |
| 新增识别引擎 / 音频源 | 在 `internal/` 下实现 `plugin.Recognizer` 或 `plugin.AudioSource` → 在 `desktop_service.go` 的 `builtinPlugins` 加一条（`registerBuiltins` 与多路音频的识别器工厂共用这张表）→ 需要额外设置项时在 `recognizerOptions` / `audioOptions` 返回 `ConfigField` |
| 多路音频 / 说话人 | 通道解析在 `internal/config/channels.go`；运行时编排在 `internal/job/manager.go` 的 `prepareChannelsLocked` 与 `startChannels`/`stopChannels`；写入走 `desktop_service.go` 的 `SetAudioChannels`（不经过 `SetConfig`）；界面在 `SettingsView.vue` 的音频源页和 `MessageStream.vue`（正在说的每路一条 `.message.live`、历史条目的说话人标签与筛选 chip），颜色令牌是 `styles.css` 的 `--speaker-1..4` |
| 主窗口三栏 | 外壳、标题栏、锁定/隐藏、栏宽拖拽与窄窗折叠都在 `views/ConsoleView.vue`；三栏内容分别是 `components/MessageStream.vue`、`InsightList.vue`、`AssistantChat.vue`。栏宽与显隐存在 localStorage 的 `kspeech.console.layout`（窗口家具，不进配置库） |
| 插件设置表单 | 后端返回的 `ConfigField` 列表由 `PluginFields.vue` 统一渲染，支持 text/password/file/folder/number/checkbox/select/message |
| 新增后端方法 | 在 `DesktopService` 上导出方法 → 重新生成绑定（生产构建脚本会自动执行）→ 在 `frontend/src/api.ts` 暴露 |
| 标点规则 | 句末判定在 `internal/punctuation/rules.go`；模型后端在 `model_sherpa.go`（`sherpa` 标签）与 `model_stub.go`；接入点是 `internal/job/manager.go` 的 `punctuate`，只作用于定稿的整句 |
| 热词 / ITN | 词表是 `assets/hotwords.txt`，规则文件是 `assets/itn_zh_number.fst`，都由 `build-windows.ps1` 复制到 `publish/`；首次运行由 `desktop_service.go` 的 `seedBuiltinRecognizerAssets` 写进 sherpa 插件配置（只种一次，路径失效会自愈）。改词表后必须跑 `python scripts/check-hotwords.py assets/hotwords.txt <模型目录>`：注释行会让原生库直接退出，英文必须全大写，阿拉伯数字与标点会让整行作废 |
| 新增市场资源类型 | `internal/resource/types.go` 加 `ModuleType*` 与路径 DTO → `install.go` 的 `validateModuleRuntimePaths`/`validateInstalledRuntimeFiles` 各加一处 → `marketplace/marketplace.json` 与 `marketplace.schema.json`（`type` 枚举 + 条件必填）→ 需要在界面里选的，在 `refreshResources` 里解析成绝对路径缓存进 `ResourceItem`，再经快照给前端。**索引走 `DefaultMarketURL`（GitHub raw master），本地改完必须推到 master 才会在应用里出现** |
| 新增市场条目 | 只改 `marketplace/marketplace.json`。上游是一个压缩包就 `download` + `extract`；是散文件（如 HuggingFace 上的 X-ASR）就每个文件一对 `download` + `save_file`，`savePath` 是模块内的相对路径。`sha256` 别手算：HuggingFace 的 `/api/models/<repo>/tree/<rev>/<dir>?expand=true` 返回的 `lfs_oid` 就是文件 sha256（非 LFS 的小文件才需要自己下载算）。改完跑 `go test ./internal/resource/ -run TestShippedMarketplaceCatalogue`，它按 `Install` 的同一套规则校验整个索引；想真连一次网络验证传输链路则 `KSPEECH_INSTALL_CHECK=1 go test ./internal/resource/ -run TestSaveFileAgainstTheRealHost` |
| 图标 / 品牌资源 | 原图放 `imgs/`，`python scripts/make-brand-assets.py` 导出 `assets/*.ico`、`frontend/src/assets/brand-mark*.png`、`installer/wizard-*.bmp`、`imgs/banner.png`；EXE 图标资源 `rsrc_windows_amd64.syso` 另外生成，见 `Develop.md` 的「图标与品牌资源」 |
| AI 助手行为 | 提示词与要点解析在 `internal/assistant/prompt.go`；触发节奏（汇总间隔、问答冷却、并发上限、对话预算）在 `assistant.go` 顶部常量；问句判定在 `question.go`；转录喂给助手的入口是 `desktop_service.go` 的 `observeTranscript` |
| AI 对话上下文 | 问答是一整条线程，**不限轮数**：`assistant.go` 的 `threadLocked` 把最近的完成轮次按 `maxThreadRunes` 预算原样带上，超出的由 `compressThreadLocked` 调一次模型压成 `threadDigest`（≤`maxDigestRunes`），摘要并进系统提示（多数网关只认一条 system）。预算在**组装请求时**就截断，压缩失败也不会撑爆请求；清空对话会连摘要一起清掉。历史轮次不重复携带各自的转写，只有当前问题带最新转写 |
| 自动问答的误报 | `question.go` 按「末尾子句 + 标记强度 + 前文抑制词」判定：强标记直接成立，弱标记要短句且指向听者或落在句尾，`介绍一下`这类祈使请求要求指向对方（`我来介绍一下`不算）。**问号不是免检**——`internal/punctuation/rules.go` 会用自己那套窄标记表补 `？`，所以带问号的句子仍要过一遍抑制词。重复提问由 `alreadyAnsweredLocked` 拦掉 |
| 新增服务商内置工具 | 在 `internal/assistant/tools.go` 的 `providerRules` 加一条：`hosts` 是官方域名（权威），`models` 是中转网关兜底用的模型名片段；工具进 `Spec`（写入 `tools` 数组）或 `Params`（合并到请求体顶层）。没有托管工具的服务商也要登记，用 `Note` 说明原因，避免每次请求都白发一个必然被拒的声明 |
| 界面样式 | 只改 `frontend/src/styles.css` 的令牌与类；组件内不写死颜色 |

详见 `docs/go-rewrite.md`（架构、兼容边界、未完成验收项）与 `Develop.md`（构建与调试）。
