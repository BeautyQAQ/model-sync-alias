# AxonHub 模型同步插件

`axonhub-model-sync` 是一个按 CLIProxyAPI 7.2.138 SDK 构建的原生插件，用于将某个 OpenAI 兼容供应商的 `models` 列表与其 `/models` 接口保持同步。

项目地址：<https://github.com/BeautyQAQ/model-sync-alias>

## 运行要求与本地配置

- CLIProxyAPI 7.2.138 的 Linux 动态插件版本。
- Go 1.26。
- 支持 CGO 的 C 编译器，例如 GCC 或 Clang。
- 插件和 CPA 主程序必须面向相同的操作系统及 CPU 架构构建。

仓库不包含 Go 工具链、CPA 主程序、私有配置或密钥。可以复制示例环境文件并按本机情况填写：

```bash
cp .env.example .env
chmod 600 .env
```

`.env` 已被 `.gitignore` 排除。需要使用其中的变量时，可在当前终端加载：

```bash
set -a
. ./.env
set +a
```

不要把真实 API 密钥、管理密钥、私网地址或用户目录写进 README、测试代码及其他会提交的文件。

## 工作方式

- 在启动时以及按照可配置的时间间隔，同步名称完全匹配的供应商（默认为 `AxonHub`）。
- 按顺序尝试供应商当前启用的 API 密钥，支持自定义请求头；代理配置优先使用密钥级代理，其次使用 CPA 全局代理。
- 仅接受 HTTP 2xx 响应，且响应中的 `data` 字段必须为数组。网络错误、HTTP 错误或响应结构错误都不会更改 CPA 配置。
- 将合法的空 `data` 数组视为权威结果，并清空该供应商的模型列表。
- 对上游模型 ID 进行不区分大小写的去重，并将真实的上游 ID 保存到 `name` 字段。
- 根据精确覆盖规则、按顺序执行的正则覆盖规则、内置模型家族规则以及保守的渠道名称清理规则，重新计算每个模型的别名。
- 使用重复别名作为 CPA 原生模型池，同时保留 `gpt-oss-20b` 和 `gpt-oss-120b` 等语义不同的模型。
- 对仍然存在的上游模型 ID，保留其别名以外的元数据。
- 如果生成的模型列表没有变化，则不会重写配置文件。

启动时的首次写入会延迟一秒，以确保 CPA 的文件监听器已经开始工作。CPA 7.2.138 通过 inode 监听配置文件，因此插件在最终提交时会保持该 inode 不变：插件先校验同目录下的临时文件，再创建并 `fsync` 变更前的备份，随后写入并 `fsync` 当前配置文件对应的 inode；如果提交失败，则恢复之前的内容。当插件从上游获取模型目录时，如果其他进程修改了配置文件，内容哈希校验会中止本次写入。

## 配置

```yaml
plugins:
  enabled: true
  dir: /absolute/path/to/cliproxyapi/plugins
  configs:
    axonhub-model-sync:
      enabled: true
      provider: AxonHub
      config_path: /absolute/path/to/cliproxyapi/config.yaml
      interval: 3h
      sync_on_start: true
      request_timeout: 30s
      backup_retention: 30
      exact_overrides: {}
      regex_overrides: []
```

请将示例路径替换为本机的绝对路径。`plugins.dir` 指向 CPA 加载 `.so` 的目录，`config_path` 指向 CPA 正在使用的 YAML 配置文件；两者都不应指向本项目源码目录。插件配置不会展开 `$CPA_DIR` 等 Shell 变量，因此不能直接在 YAML 中使用环境变量占位符。

精确覆盖规则的值如果为空，则保留原始的上游模型 ID。正则规则会按配置顺序执行，并使用 Go 正则表达式的替换语法；第一条匹配的规则生效。若没有正则规则匹配（包括未配置或配置为空列表），插件会继续执行 `normalize.go` 中现有的内置模型族规则和通用清理逻辑：

```yaml
exact_overrides:
  vendor/special-model: official-model
regex_overrides:
  - pattern: '^vendor/(.*)$'
    replacement: '$1'
```

精确覆盖规则的优先级高于内置模型家族规则。插件默认会区分 `gpt-oss-20b` 和 `gpt-oss-120b`；如果配置了以下规则，则会主动把 20B 模型合并进 120B 的别名池：

```yaml
exact_overrides:
  gpt-oss-20b: gpt-oss-120b
  openai/gpt-oss-20b: gpt-oss-120b
```

如果希望两个模型保持独立，应从 CPA 配置中删除这两条覆盖规则。这个行为由配置决定，不需要修改插件代码。

### 自托管插件商店

本仓库根目录的 `registry.json` 可以直接作为 CPA 第三方插件源使用：

```yaml
plugins:
  enabled: true
  dir: plugins
  store-sources:
    - https://raw.githubusercontent.com/BeautyQAQ/model-sync-alias/main/registry.json
```

版本标签（例如 `v1.0.2`）会触发 GitHub Actions，在 GitHub Release 中发布适用于 Linux amd64 的插件 ZIP 和 `checksums.txt`。商店展示版本以最新 GitHub Release 为准；`registry.json` 中的 `version` 仅作为 Release 元数据不可用时的展示回退。

## 管理 API

所有接口都位于需要身份验证的 CPA 管理 API 下：

- `GET /v0/management/plugins/axonhub-model-sync/status`
- `POST /v0/management/plugins/axonhub-model-sync/preview`
- `POST /v0/management/plugins/axonhub-model-sync/sync`

接口响应不会包含上游 API 密钥、自定义请求头、代理 URL 或供应商的基础 URL。

### 使用示例

先通过本地环境变量提供管理 API 地址和密钥。示例文件中的 `CPA_MANAGEMENT_KEY` 为空，真实密钥只能填写在不提交的 `.env` 中：

```bash
CPA_MANAGEMENT_URL="${CPA_MANAGEMENT_URL:-http://127.0.0.1:8317}"
: "${CPA_MANAGEMENT_KEY:?请先设置 CPA_MANAGEMENT_KEY}"
```

查看状态，不请求 AxonHub：

```bash
curl -sS \
  -H "X-Management-Key: ${CPA_MANAGEMENT_KEY}" \
  "${CPA_MANAGEMENT_URL}/v0/management/plugins/axonhub-model-sync/status"
```

预览同步结果。该操作会访问 AxonHub 的 `/models` 接口，但不会修改配置：

```bash
curl -sS -X POST \
  -H "X-Management-Key: ${CPA_MANAGEMENT_KEY}" \
  -H 'Content-Type: application/json' \
  -d '{}' \
  "${CPA_MANAGEMENT_URL}/v0/management/plugins/axonhub-model-sync/preview"
```

立即执行同步。如果模型列表存在变化，该操作会先备份再修改配置：

```bash
curl -sS -X POST \
  -H "X-Management-Key: ${CPA_MANAGEMENT_KEY}" \
  -H 'Content-Type: application/json' \
  -d '{}' \
  "${CPA_MANAGEMENT_URL}/v0/management/plugins/axonhub-model-sync/sync"
```

主要响应字段含义：

- `upstream_count`：AxonHub 当前返回的唯一模型数量。
- `configured_count`：当前 CPA 配置中的模型数量。
- `added_count`、`removed_count`：状态对象中的新增和删除数量。
- `diff.added`、`diff.removed`：预览或同步响应中准备新增、删除的真实上游模型名称。
- `alias_changes`：需要重新映射别名的模型数量。
- `alias_pool_count`：由多个渠道共用同一别名所形成的模型池数量。
- `changed`：模型配置是否存在差异。
- `applied`：本次操作是否实际写入了配置。
- `last_error`：最近一次同步错误。

## 项目结构

```text
.
├── cmd/
│   └── axonhub-model-sync/
│       └── main.go                    # CGO/C ABI 共享库入口及宿主日志桥接
├── internal/
│   ├── plugin/
│   │   ├── service.go                 # 插件注册、生命周期和管理 API 协议
│   │   └── service_test.go            # 插件协议层单元测试
│   └── syncer/
│       ├── manager.go                 # 定时任务、同步编排及运行状态
│       ├── settings.go                # 插件配置解析、默认值及校验
│       ├── source.go                  # CPA 配置读取及上游 /models 请求
│       ├── normalize.go               # 模型名称归一化和别名生成
│       ├── diff.go                    # 当前模型与目标模型差异计算
│       ├── persist.go                 # 配置校验、备份及安全写入
│       ├── *_test.go                  # 同步核心的白盒单元测试
│       └── testdata/
│           └── current_models.golden  # 模型归一化测试基准数据
├── tests/
│   └── integration/
│       ├── integration_test.go        # 独立 CPA 进程端到端测试
│       ├── live_preview_test.go       # 真实配置的只读同步预览
│       └── live_catalog_test.go       # 运行中 CPA 模型目录检查
├── .env.example                       # 本地构建和测试环境变量模板
├── go.mod                             # Go 模块及直接依赖
├── go.sum                             # 依赖校验信息
└── README.md                          # 使用、开发和部署文档
```

生产代码的依赖方向为 `cmd/axonhub-model-sync → internal/plugin → internal/syncer`。`internal/syncer` 中的 `*_test.go` 是对应包的白盒单元测试，测试夹具位于该包的 `testdata/`；Go 会自动忽略 `testdata` 目录中的内容，不会将其编译进插件。需要 CPA 进程或真实配置的测试统一放在 `tests/integration`，并默认跳过。

## 构建与验证

默认使用 `PATH` 中的 `go` 和 `cc`。如果工具链安装在其他位置，可在 `.env` 中配置 `GO_BIN` 和 `CC`：

```bash
GO_BIN="${GO_BIN:-go}"
CC="${CC:-cc}"

"$GO_BIN" version
"$CC" --version

CC="$CC" CGO_ENABLED=1 "$GO_BIN" test ./...
CC="$CC" CGO_ENABLED=1 "$GO_BIN" test -race ./...
CC="$CC" CGO_ENABLED=1 "$GO_BIN" vet ./...
CC="$CC" CGO_ENABLED=1 "$GO_BIN" build \
  -buildmode=c-shared \
  -trimpath \
  -ldflags=-s \
  -o axonhub-model-sync.so \
  ./cmd/axonhub-model-sync
```

本地 Go 工具链目录会被 `.gitignore` 排除。建议将工具链安装在仓库外部，避免 `go test ./...`、`go mod tidy` 和编辑器索引误扫描 Go 发行版源码。

进程级集成测试默认不会运行，需要显式启用。该测试会在临时目录中启动独立的 CPA 进程，不会修改实际 CPA 配置：

```bash
GO_BIN="${GO_BIN:-go}"
CC="${CC:-cc}"
: "${CPA_BINARY:?请先设置 CPA_BINARY}"

CPA_PLUGIN_SO="${CPA_PLUGIN_SO:-$PWD/axonhub-model-sync.so}"

CPA_INTEGRATION=1 \
CPA_BINARY="$CPA_BINARY" \
CPA_PLUGIN_SO="$CPA_PLUGIN_SO" \
CC="$CC" \
CGO_ENABLED=1 \
"$GO_BIN" test -run '^TestCPAPluginEndToEnd$' -v ./tests/integration
```

`TestLivePreview` 和 `TestLiveCPACatalog` 会读取真实 CPA 配置并访问运行中的服务，因此必须单独显式启用，不能在 `.env` 中默认设置 `AXONHUB_LIVE_CONFIG`：

```bash
: "${CPA_CONFIG_PATH:?请先设置 CPA_CONFIG_PATH}"

AXONHUB_LIVE_CONFIG="$CPA_CONFIG_PATH" \
"$GO_BIN" test -run '^(TestLivePreview|TestLiveCPACatalog)$' -v ./tests/integration
```

## 安装或更新插件

构建完成后，项目目录中的 `axonhub-model-sync.so` 才是待部署文件。`-buildmode=c-shared` 同时生成的 `axonhub-model-sync.h` 不需要复制到 CPA 目录。

更新已运行的原生插件时，应先停止 CPA，再覆盖 `.so`，最后重新启动服务，避免在共享库仍被加载时直接改写文件。以下命令以 systemd 用户服务为例：

```bash
PROJECT_DIR="${PROJECT_DIR:-$PWD}"
CPA_SERVICE="${CPA_SERVICE:-cliproxyapi.service}"
: "${CPA_DIR:?请先设置 CPA_DIR}"

systemctl --user stop "$CPA_SERVICE"
install -m 0755 \
  "$PROJECT_DIR/axonhub-model-sync.so" \
  "$CPA_DIR/plugins/axonhub-model-sync.so"
systemctl --user start "$CPA_SERVICE"
systemctl --user status "$CPA_SERVICE" --no-pager
```

服务启动后，可以通过前述状态接口确认插件已经正确加载。不要在 CPA 正在运行时直接把构建输出写入其插件目录。

自动备份会写入 CPA 配置文件旁的 `config_backup/axonhub_sync_*.yaml`。只有使用该前缀的备份文件才受已配置的保留数量限制。
