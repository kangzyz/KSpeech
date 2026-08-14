# 模型市场索引

`marketplace.json` 是 KSpeech 资源页读取的模型目录。

应用默认读取的地址写在 `internal/resource/types.go` 的 `DefaultMarketURL`：

```
https://raw.githubusercontent.com/kangzyz/KSpeech/master/marketplace/marketplace.json
```

**改动本目录会立即对所有已发布版本生效**，因为这个地址是运行时拉取的，不随版本打包。
raw.githubusercontent.com 有几分钟的 CDN 缓存，改完不会立刻可见。

## 这里只放索引

每个模块的 `install[].url` 都是指向 sherpa-onnx releases 的**绝对地址**，模型本体
（每个 300–500 MB）仍从上游下载，本仓库不存放任何模型文件。因此自建市场的成本
只有这一个几 KB 的 JSON。

代码要求所有 URL 必须是 HTTPS 且带 host（见 `internal/resource/network.go` 的
`validateResourceURL`），相对地址会被直接拒绝。

## 新增一个模型

1. 在 `modules` 里加一条，`id` 全局唯一，`version` 用整数（通常是 `YYYYMMDD`），
   `displayVersion` 是给人看的版本号。资源页靠 `version` 变大来判断“可更新”。
2. `install` 一般是三步：`download` → `extract` → `write_module_json`。
3. `sherpaonnx` / `sherpancnn` 段给出解压后各文件的相对路径，注意目录前缀通常是
   压缩包内的顶层目录名。
4. 补上 `sha256`，见下一节。

字段定义见同目录的 `marketplace.schema.json`（`marketplace.json` 用相对路径引用它，
所以两个文件必须放在一起）；Go 侧的结构体在 `internal/resource/types.go`。

## sha256

`InstallStep.SHA256` 是可选字段，但本索引为所有条目都填了：填了之后
`internal/resource/install.go` 会校验下载到的字节，不匹配就中止安装并回滚。

取值方式：

```bash
# GitHub 有时会在 release asset 的 digest 字段直接给出
curl -s https://api.github.com/repos/k2-fsa/sherpa-onnx/releases/tags/asr-models \
  | jq -r '.assets[] | select(.name=="<文件名>") | .digest'

# 没有 digest 时流式计算，不用落盘
curl -sL <url> | sha256sum
```

摘要只保证拿到的字节和记录一致，不能替代 HTTPS，也不比记录它的这份 JSON 更可信。
