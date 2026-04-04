# CodeScan

CodeScan 是一个面向源码安全审计场景的 Go + Vue 平台，支持项目上传、路由梳理、分阶段漏洞审计、结果复核与 HTML 报告导出。

它的目标不是只给出一份静态扫描结果，而是把“接口梳理 -> 分阶段审计 -> 结果复核 -> 报告导出”做成一套可持续操作的审计工作台。

## 核心特点

- 仪表盘总览：集中查看项目数量、接口规模、漏洞数量、审计完成情况与风险分布。
- 路由分析：先梳理项目路由，再进入具体审计阶段，减少盲扫。
- 多阶段审计：支持 `RCE`、`注入`、`认证与会话`、`访问控制`、`XSS`、`配置与组件`、`文件操作`、`业务逻辑` 等阶段化审计。
- 结果复核：支持补扫、复核发现、区分确认与不确定结果，便于持续收敛误报。
- 细节下钻：可查看漏洞描述、调用链、触发接口、HTTP POC 等详细信息。
- 报告导出：支持导出整合后的 HTML 报告，方便交付与留档。
- 开源发布保护：内置发布检查，避免把 `config.json`、任务数据、用户上传项目和构建产物误传到 GitHub。

## 界面演示

### 1. 总览仪表盘

![CodeScan 总览仪表盘](png/1.png)

展示系统运行状态、项目总数、发现接口数、漏洞数量、完成审计数量，以及风险等级和阶段完成情况。

### 2. 项目详情与阶段工作流

![CodeScan 项目详情与阶段工作流](png/2.png)

按项目进入审计面板后，可以直接切换各个安全审计阶段，并查看路由分析结果与阶段执行状态。

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

执行初始化后，程序会自动生成 `auth_key` 并写入本地 `data/config.json`，不需要手动在示例配置里填写固定值。

启动后端：

```bash
go run .
```

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

## 配置说明

- 实际运行配置请保存在本地 `data/config.json`。
- 开源仓库中提供的是安全示例文件 `data/config.example.json`。
- `auth_key` 会在执行 `go run ./cmd/init` 时自动生成并写入本地配置文件。
- 后端支持通过环境变量覆盖关键配置，例如：
  - `CODESCAN_AUTH_KEY`
  - `CODESCAN_DB_PASSWORD`
  - `CODESCAN_AI_API_KEY`
- `data/config.json` 属于本地私有文件，不能公开发布。

## 开源发布安全

在推送代码或打包发布前，建议先执行内置检查：

```bash
go run ./cmd/release check
```

该检查会自动排除以下本地或产物内容：

- `data/config.json`
- `data/tasks.json`
- `projects/`
- `frontend/node_modules/`
- `frontend/dist/`
- `frontend/.cache/`
- `frontend/.vite/`
- `bin/`
- `release/`
- 现有 `*.zip`、`*.exe` 等构建产物

如需导出开源发布包：

```bash
go run ./cmd/release export -out release/CodeScan-open-source.zip
```

导出过程会再次校验 ZIP 内容，避免把敏感配置和不应公开的文件打进去。
