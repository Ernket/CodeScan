# CodeScan

CodeScan 是一个面向源码安全审计场景的 Go + Vue 平台，支持项目上传、路由梳理、分阶段漏洞审计、结果复核、HTML 报告导出、多用户登录与组织级项目隔离。

当前版本已经切到基于 LangGraph 的审计编排，把“路由识别 -> 阶段专项扫描 -> 每个阶段单独验证漏洞有效性 -> 汇总复核 -> 报告导出”做成一套可持续操作的审计工作台。

本次更新完成了前后端一体化发布能力：开发阶段仍然可以用 Vite 代理 `/api`，发布阶段可以把 Vue 构建产物嵌入 Go 程序，由同一个 `codescan.exe` 同时提供页面和后端接口。

## 核心特点

- 仪表盘总览：集中查看项目数量、接口规模、漏洞数量、审计完成情况与风险分布。
- 路由分析：先梳理项目路由，再进入具体审计阶段，减少盲扫。
- LangGraph 编排：由初始化阶段先沉淀路由与模块线索，再驱动后续阶段专项扫描与验证链路。
- 多阶段审计：支持 `RCE`、`注入`、`认证与会话`、`访问控制`、`XSS`、`配置与组件`、`文件操作`、`业务逻辑` 等阶段化审计。
- 结果复核：每个阶段的发现都会进入独立验证流程，区分确认与不确定结果，便于持续收敛误报。
- 细节下钻：可查看漏洞描述、调用链、触发接口、HTTP POC 等详细信息。
- 多用户登录：支持账号密码登录、token 鉴权、账号启停、密码重置和角色权限控制。
- 组织架构划分：支持树形组织、用户组织授权、项目按组织归属隔离，上传、启动、复核等操作会按组织权限校验。
- 前后端合并发布：支持将前端 `dist` 嵌入 Go 可执行文件，生产环境只需要启动后端程序即可访问完整 Web 控制台。
- 上下文压缩策略内建：只保留模型上下文窗口配置，micro/full/hard 等压缩触发阈值由程序根据窗口大小自动推导。
- 报告导出：支持导出整合后的 HTML 报告，方便交付与留档。

## 界面演示

### 1. 总览仪表盘

![CodeScan 总览仪表盘](png/1.png)

展示系统运行状态、项目总数、发现接口数、漏洞数量、完成审计数量，以及风险等级和阶段完成情况。

### 2. LangGraph 编排工作台

![CodeScan LangGraph 编排工作台总览](png/bp1.png)

![CodeScan LangGraph 编排阶段矩阵](png/bp2.png)

新版工作台已经切到 LangGraph 编排：先完成路由识别与初始化沉淀，再按阶段推进专项扫描；每个阶段都会单独执行漏洞有效性验证，最后再汇总到面板与报告中。

### 3. 注入审计结果总览

![CodeScan 注入审计结果总览](png/3.png)

在具体审计阶段内，可以集中查看有效发现、风险等级、复核状态，以及每条问题的核心说明。

### 4. 漏洞详情与 HTTP POC

![CodeScan 漏洞详情与 HTTP POC](png/4.png)

支持下钻查看漏洞触发接口、关键执行逻辑、调用链片段与 HTTP POC，方便验证与复测。

## 技术栈

- 后端：Go
- 前端：Vue 3 + Vite
- UI：Tailwind CSS
- 数据库：MySQL

## 快速开始

### 环境要求

- Go 1.23.3+
- Node.js 20+
- MySQL

### Ubuntu 升级 Go

如果 Ubuntu 环境中的 Go 版本过低，可以执行下面的命令升级到 `go1.23.3`：

```bash
cd /tmp && wget https://go.dev/dl/go1.23.3.linux-amd64.tar.gz && sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf go1.23.3.linux-amd64.tar.gz && grep -qxF 'export PATH=/usr/local/go/bin:$PATH' ~/.bashrc || echo 'export PATH=/usr/local/go/bin:$PATH' >> ~/.bashrc && export PATH=/usr/local/go/bin:$PATH && go version
```

该命令适用于 Ubuntu，并会直接替换 `/usr/local/go` 下现有的 Go 安装。

### 1. 初始化后端

初始化本地目录与配置：

```bash
go run ./cmd/init
```

执行初始化后，程序会自动生成 token 签名密钥 `auth_key` 并写入本地 `data/config.json`，不需要手动在示例配置里填写固定值。`auth_key` 仅用于服务端签发/校验登录 token，不再作为登录密码。

初始化还会确保存在一个超级管理员账号：默认用户名为 `admin`。默认密码优先读取环境变量 `CODESCAN_ADMIN_PASSWORD`；未设置时会随机生成并在初始化输出中显示一次。

如果本地已经存在旧版 `data/config.json`，且仍保留已移除字段，`go run ./cmd/init` 会直接报错并指出具体字段路径，需要先清理旧 key 再继续。

启动后端：

```bash
go run .
```

后端默认监听 `http://localhost:8089/`。首次登录后建议先由超级管理员创建组织，再创建普通用户并分配组织权限；新建项目上传时必须选择一个当前用户具备写权限的组织。

### 2. 启动前端

```bash
cd frontend
npm install
npm run dev
```

如需打包前端：

```bash
cd frontend
npm run build
```

开发模式下，Vite 会把 `/api` 请求代理到 Go 后端；如果使用一体化发布包，则不需要单独启动前端开发服务。

### 3. 可选安装 `rg`

代码搜索工具对上层仍保留 `grep` 接口名，但底层实现已经默认优先使用 `ripgrep (rg)`；如果系统里没有 `rg`，会自动回退到内置的 Go 扫描实现，不需要手动切换，只是性能和结果规模会相对保守一些。

常见安装方式：

```bash
# Ubuntu / Debian
sudo apt-get update && sudo apt-get install -y ripgrep

# macOS (Homebrew)
brew install ripgrep

# Windows (winget)
winget install BurntSushi.ripgrep.MSVC
```

## 配置说明

- 实际运行配置请保存在本地 `data/config.json`。
- 开源仓库中提供的是安全示例文件 `data/config.example.json`。
- `auth_key` 是服务端 token 签名密钥，会在执行 `go run ./cmd/init` 时自动生成并写入本地配置文件；登录请使用账号密码。
- 配置解析为严格模式，未知字段或已移除字段会在启动时直接报错，不再静默忽略。
- 上下文压缩只需要通过 `scanner_config.context_compression.context_window_tokens` 配置模型上下文窗口；micro/full/hard 触发阈值、字节回退阈值、摘要窗口、微压缩细节、编排心跳、角色并发等低价值调参已经内建到代码中。
- 当前支持的配置结构如下：

```json
{
  "auth_key": "...",
  "db_config": {
    "host": "127.0.0.1",
    "port": 3306,
    "user": "root",
    "password": "",
    "dbname": "codescan"
  },
  "ai_config": {
    "api_key": "replace-with-api-key",
    "base_url": "https://api.openai.com/v1",
    "model": "gpt-5.4",
    "thinking": {
      "enabled": true,
      "effort": "high",
      "max_completion_tokens": 20000,
      "apply_to_auxiliary": true
    }
  },
  "scanner_config": {
    "context_compression": {
      "context_window_tokens": 128000
    },
    "session_memory": {
      "enabled": true
    }
  },
  "orchestration_config": {
    "enabled": true,
    "worker": {
      "model": ""
    },
    "validator": {
      "model": ""
    }
  }
}
```

- 后端支持通过环境变量覆盖关键配置，例如：
  - `CODESCAN_AUTH_KEY`
  - `CODESCAN_DB_PASSWORD`
  - `CODESCAN_AI_API_KEY`
  - `CODESCAN_AI_BASE_URL`
  - `CODESCAN_AI_MODEL`
  - `CODESCAN_AI_THINKING_ENABLED`
  - `CODESCAN_AI_REASONING_EFFORT`
  - `CODESCAN_AI_MAX_COMPLETION_TOKENS`
- `data/config.json` 属于本地私有文件，不能公开发布。

## 账号与组织权限

- `super_admin`：超级管理员，可以管理用户、组织和所有项目，也可以删除项目。
- `admin`：普通管理员，可以读取已授权组织及其子组织的项目；拥有组织 `admin` 授权时，可以在该组织范围内上传项目、启动扫描、阶段重跑、复核和修复输出。
- `observer`：观察者，可以读取已授权组织及其子组织的项目，但不能执行写操作。
- 组织授权分为 `member` 和 `admin`。授权会沿组织树向子组织生效，方便按团队、部门或客户空间划分审计项目。
- 禁用账号或重置密码会提升 token 版本，旧 token 会失效。

## 前后端一体化发布

日常开发仍然保持前后端分离：启动 Go 后端，再在 `frontend` 目录执行 `npm run dev`。

发布时先构建前端，再使用 `embedded_frontend` 标签编译后端，可得到一个同时服务 Vue 页面和 `/api` 接口的可执行文件：

```powershell
npm --prefix frontend run build
go build -tags embedded_frontend -o release/windows-amd64/codescan.exe .
```

运行 `release/windows-amd64/codescan.exe` 后访问 `http://localhost:8089/`。MySQL、`data/config.json` 和初始化账号要求与开发模式一致。
