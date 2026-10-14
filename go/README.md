# Postleaks Go

独立的纯 Go 命令行版本，保留仓库根目录的 Python 应用。运行时无需 Python、Whispers 或外部配置文件。

## 编译

需要 Go 1.22 或更新版本，可从 [Go 官网](https://go.dev/dl/) 安装。先执行 `go version` 确认安装成功。首次编译需要联网下载 Go 依赖；运行编译后的程序无需安装 Go、Python 或 Whispers，也无需安装 C 编译器。

### macOS / Linux：编译当前系统版本

在仓库根目录 `postleaks/` 执行以下命令；如果已经进入 `postleaks/go/`，跳过 `cd go`：

```bash
cd go
go version
go mod download
mkdir -p bin
CGO_ENABLED=0 go build -o bin/postleaks ./cmd/postleaks
```

编译成功后生成 `postleaks/go/bin/postleaks`。此时仍在 `go/` 目录，可以查看帮助或开始搜索：

```bash
./bin/postleaks -h
./bin/postleaks -k example.com --output output.json
```

第二条命令将结果写入当前目录的 `output.json`。Apple Silicon Mac 会默认生成 macOS ARM64 程序；Intel Mac 会默认生成 macOS AMD64 程序。Linux 同样默认编译当前机器的系统和架构。

### Windows：PowerShell 编译

在仓库根目录执行；如果已经进入 `go/`，跳过 `Set-Location go`：

```powershell
Set-Location go
go version
go mod download
New-Item -ItemType Directory -Force bin | Out-Null
$env:CGO_ENABLED = "0"
go build -o bin/postleaks.exe ./cmd/postleaks
.\bin\postleaks.exe -h
.\bin\postleaks.exe -k example.com --output output.json
```

编译产物为 `postleaks/go/bin/postleaks.exe`。

### macOS / Linux：编译其他平台版本

在 `go/` 目录执行，按需选择一条命令：

```bash
mkdir -p bin

# Linux x86-64
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/postleaks-linux-amd64 ./cmd/postleaks

# Linux ARM64
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o bin/postleaks-linux-arm64 ./cmd/postleaks

# Windows x86-64
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o bin/postleaks-windows-amd64.exe ./cmd/postleaks

# macOS Apple Silicon
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o bin/postleaks-darwin-arm64 ./cmd/postleaks

# macOS Intel
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -o bin/postleaks-darwin-amd64 ./cmd/postleaks
```

将对应产物复制到目标系统运行。分发二进制时须一并提供仓库根目录的 GPL-3.0 `LICENSE`、`internal/detector/NOTICE` 和 `internal/detector/WHISPERS_LICENSE`。

### 常见编译问题

- `go: command not found`：安装 Go，并重新打开终端，使 Go 命令进入 `PATH`。
- 提示找不到 `go.mod`：先进入仓库的 `go/` 目录再编译，根目录是 Python 项目。
- 下载依赖失败：检查网络及当前 Go 模块代理配置，修复后重新执行 `go mod download`。
- 目标机器无法运行：确认使用了匹配目标系统与 CPU 架构的产物。

## 使用

```bash
./bin/postleaks -h
./bin/postleaks -k example.com --output output.json
./bin/postleaks -kf keywords.txt --extend-workspaces --strict --output output.json
./bin/postleaks -k example.com --include /api --exclude /public --raw --output output.json
./bin/postleaks -k example.com --debug --output output.json
```

| 参数 | 行为 |
| --- | --- |
| `-k KEYWORD` | 搜索域名、公司等关键词 |
| `-kf FILE` | 从文件读取关键词，每行一个；可与 `-k` 同时使用 |
| `--extend-workspaces` | 将搜索扩展到命中请求所在工作区的集合 |
| `--strict` | URL 必须包含当前关键词 |
| `--include TEXT` | URL 必须包含指定字符串 |
| `--exclude TEXT` | URL 不得包含指定字符串 |
| `--raw` | 标准输出打印 JSON，每行一个请求对象 |
| `--output PATH` | 全部关键词的结果合并到一个 JSON 数组文件 |
| `--debug` | 标准错误打印完整 HTTP 请求和响应 |
| `-h`、`--help` | 显示帮助 |

关键词去除首尾空白并忽略空行、重复项。所有 URL 匹配均不区分大小写。启用 URL 筛选时，空、缺失或非字符串 URL 不通过筛选。

默认输出文件为 `results_<Unix时间戳>.json`，自动创建父目录。跨关键词按请求 ID 去重，后获取的详情覆盖之前的内容，结果按 ID 排序。JSON 保留原版字段、嵌套数据、中文，以及缺失字段与 `null` 的区别；没有结果时写入 `[]`。

进度、错误、HTTP 调试和敏感信息提示输出到标准错误。普通展示支持终端颜色，设置 `NO_COLOR` 可禁用；`--raw` 输出始终是无颜色的 JSON。终端输出可保留同一请求在不同关键词下的多次命中，导出文件会去重。

详情按请求 ID 顺序串行获取，每获取一条符合筛选条件的结果就立即显示。获取前会打印请求总数以及 `Fetching request 1/25 (ID:...)` 等进度；重试时会打印失败原因、尝试次数和等待时间。单条请求在三次均超时的情况下可能耗时约 93 秒，整个任务耗时取决于请求数量和网络响应速度。需要查看具体 HTTP 交互时可加 `--debug`。

退出码：`0` 表示完成（包括空结果），`1` 表示运行失败，`2` 表示参数无效。详情、后续分页或工作区获取失败时提示并继续；初始搜索失败会终止。遇到终止错误时，保存已经收集的结果并返回非零状态。

## 功能基准

Postman 请求格式、请求头及接口沿用当前 Python 应用。搜索每页 25 条，最大偏移 200，最多访问 9 页。请求超时 30 秒，网络错误以及 HTTP 429、500、502、503、504 最多尝试 3 次；数字型 `Retry-After` 最长等待 5 秒。

敏感信息检测以 Whispers 2.2.1 为基准，内置原配置选中的 21 条内置规则及 `oauth`、`authorizations` 两条自定义规则。检测器只解析数据，不执行请求数据、描述或脚本。JSON 遍历、Postman 键值结构、URL 凭据和查询参数、ARN、变量赋值与占位符过滤均由 Go 实现。

相对 Python 版本，`--raw` 修正为合法 JSON，关键词空行与重复项被忽略，错误处理更稳健，输出顺序固定。原 Python 程序没有锁定 Whispers 版本，因此使用其他 Whispers 版本时，检测结果可能不同。

## 检查

```bash
go test ./...
go test -race ./...
go vet ./...
```

测试仅使用本地 HTTP 模拟服务器、普通文本和空或无效 JSON，不请求真实 Postman，不生成凭据、私钥或命令样例。当前环境此前发生 XDR 拦截，完整检测对照、最终二进制运行和跨平台编译尚未验证。
