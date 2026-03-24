# PostgreSQL 自动化部署工具 (pg-deploy)

<div align="center">

**🐘 一款功能强大的 PostgreSQL 自动化部署工具**

[![Go Version](https://img.shields.io/badge/Go-1.21+-00ADD8?style=flat&logo=go)](https://golang.org/)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-14+-336791?style=flat&logo=postgresql)](https://www.postgresql.org/)
[![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

支持 **单机、主从、Patroni 高可用、Citus 分布式** 四种部署模式

适用于 **10-50 节点** 的生产环境

</div>

---

## 📋 目录

- [特性](#特性)
- [快速开始](#快速开始)
- [部署模式](#部署模式)
- [架构设计](#架构设计)
- [使用指南](#使用指南)
- [性能指标](#性能指标)
- [文档](#文档)
- [贡献](#贡献)
- [许可证](#许可证)

---

## ✨ 特性

### 🚀 核心功能

- **4 种部署模式**：单机、主从、Patroni HA、Citus 分布式
- **高并发部署**：支持 10-50 节点并发部署
- **幂等性保证**：所有操作支持重复执行
- **自动回滚**：部署失败时安全回滚
- **断点续传**：支持从中断点继续部署
- **拓扑管理**：完整的集群拓扑管理能力

### 🔧 技术特性

- **模块化设计**：清晰的职责分离，易于扩展
- **接口驱动**：统一的部署步骤接口
- **并发安全**：通过竞态检测，线程安全
- **结构化日志**：JSON 格式，支持上下文字段
- **配置管理**：模板系统，动态生成配置
- **凭证管理**：基于 .pgpass 的密码管理

---

## 🎯 快速开始

### 环境要求

- Go 1.21+
- PostgreSQL 14+
- sshpass（密码认证）
- gcc, make（编译模式）
- python3, pip（Patroni 模式）

### 安装

```bash
# 克隆仓库
git clone https://github.com/example/pg-deploy.git
cd pg-deploy

# 下载依赖
go mod download

# 编译
go build -o pg-deploy main.go
```

### 创建配置

```bash
cat > deploy.conf << EOF
ssh_user: root
ssh_password: your_password
deploy_mode: patroni
build_mode: distribute
pg_source: /path/to/pgsql.tar.gz
pg_soft_dir: /usr/local/pgsql
extensions: citus
group_0: 0|patroni0|coordinator|192.168.1.10:5432:/data/pgdata::::1,1|patroni1|coordinator|192.168.1.11:5432:/data/pgdata::::0
EOF
```

### 执行部署

```bash
# 验证连接
export PGUSER=postgres
export PGPASSWORD=your_pg_password
./validator deploy.conf

# 执行部署
./pg-deploy deploy deploy.conf
```

---

## 🏗️ 部署模式

### 1. 单机模式 (Standalone)

最简单的部署模式，适用于单台服务器。

```bash
deploy_mode: standalone
```

**特点**：
- 单个 PostgreSQL 实例
- 快速部署
- 适合开发和测试

### 2. 主从模式 (Master-Slave)

传统的流复制架构，支持读写分离。

```bash
deploy_mode: master-slave
```

**特点**：
- 一个主节点，多个从节点
- 异步流复制
- 支持同步复制（可选）
- 支持级联复制

### 3. Patroni 高可用模式

基于 Patroni 和 etcd 的自动故障转移集群。

```bash
deploy_mode: patroni
```

**特点**：
- 自动故障转移
- etcd 分布式存储
- 集群成员管理
- 配置自动同步

### 4. Citus 分布式模式

基于 Citus 扩展的分布式 PostgreSQL 集群。

```bash
deploy_mode: citus
```

**特点**：
- Coordinator + Worker 架构
- 自动分片
- 分布式查询
- 水平扩展

---

## 🏛️ 架构设计

```
┌─────────────────────────────────────────────────────┐
│                   CLI 层（计划中）                   │
│              基于 Cobra 的命令行界面                │
└─────────────────────────────────────────────────────┘
                         ↓
┌─────────────────────────────────────────────────────┐
│                   业务逻辑层                         │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐         │
│  │ 部署引擎  │  │ 拓扑管理  │  │ 配置管理  │         │
│  │ deploy/  │  │topology/ │  │ config/  │         │
│  └──────────┘  └──────────┘  └──────────┘         │
└─────────────────────────────────────────────────────┘
                         ↓
┌─────────────────────────────────────────────────────┐
│                   基础设施层                         │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐         │
│  │ 凭证管理  │  │ 并发执行  │  │ 日志系统  │         │
│  │credentials│ │ executor │  │ logger/  │         │
│  └──────────┘  └──────────┘  └──────────┘         │
└─────────────────────────────────────────────────────┘
```

---

## 📖 使用指南

### 验证连接

```bash
go run examples/validator_example.go deploy.conf
```

输出：
```
=== Connection Validation Report ===

SSH Connections:
  Total:      2
  Successful: 2
  Failed:     0

PostgreSQL Connections:
  Total:      2
  Successful: 2
  Failed:     0

All nodes passed validation!
```

### 执行部署

```bash
go run examples/deploy_example.go deploy.conf
```

输出：
```
🚀 Starting deployment...

Step 1/6: PrepareDirectories
Step 2/6: DeploySoftware
Step 3/6: InitDatabase
Step 4/6: ConfigurePostgreSQL
Step 5/6: StartPostgreSQL
Step 6/6: ValidateDeployment

✅ Deployment completed successfully!

📊 Deployment Summary:
   Total Steps: 6
   Completed: 6
   Success Rate: 100.0%
```

### 管理 Citus 集群

```go
import "github.com/example/pg-deploy/pkg/topology"

mgr := topology.NewCitusManager(cfg, exec, log)

// 配置 coordinator
mgr.ConfigureCoordinator()

// 注册 worker
mgr.RegisterWorkers()

// 创建分布式表
mgr.CreateDistributedTable("orders", "customer_id", "")

// 获取集群状态
status, _ := mgr.GetClusterStatus()
fmt.Printf("Workers: %d\n", status.TotalWorkers)
```

### 管理 Patroni 集群

```go
import "github.com/example/pg-deploy/pkg/topology"

mgr := topology.NewPatroniManager(cfg, exec, log)

// 部署 etcd
mgr.DeployEtcdCluster()

// 启动 Patroni
mgr.StartPatroniCluster()

// 获取集群成员
members, _ := mgr.GetClusterMembers()

// 执行故障转移
mgr.PerformFailover("node1", "node2")
```

---

## 📊 性能指标

### 部署性能（10 节点）

| 模式 | 耗时 | 说明 |
|------|------|------|
| 单机 | ~15 分钟 | 基础部署 |
| 主从 | ~25 分钟 | 包含复制设置 |
| Patroni | ~35 分钟 | 包含 etcd 部署 |
| Citus | ~30 分钟 | 包含 worker 配置 |

### 并发性能

| 节点数 | 并发度 | 总耗时 |
|--------|--------|--------|
| 10 | 10 | ~25 分钟 |
| 20 | 10 | ~40 分钟 |
| 50 | 10 | ~100 分钟 |

### 复制性能

| 配置 | 复制延迟 | 吞吐量 |
|------|----------|--------|
| 异步复制 | < 1s | 高 |
| 同步复制 | < 100ms | 中 |
| 级联复制 | < 2s | 高 |

---

## 📚 文档

- [快速开始指南](docs/QUICKSTART.md)
- [使用手册](docs/USER_MANUAL.md)
- [阶段一：基础设施](docs/PHASE1.md)
- [阶段二：部署引擎](docs/PHASE2.md)
- [更新记录](CHANGELOG.md)

---

## 🤝 贡献

欢迎贡献代码、报告问题或提出建议！

1. Fork 本仓库
2. 创建特性分支 (`git checkout -b feature/AmazingFeature`)
3. 提交更改 (`git commit -m 'Add some AmazingFeature'`)
4. 推送到分支 (`git push origin feature/AmazingFeature`)
5. 开启 Pull Request

### 开发规范

- 遵循 [Effective Go](https://go.dev/doc/effective_go)
- 使用 `gofmt` 格式化代码
- 添加详细的注释
- 编写单元测试
- 更新相关文档

---

## 📄 许可证

本项目采用 MIT 许可证 - 查看 [LICENSE](LICENSE) 文件了解详情。

---

## 🙏 致谢

- [PostgreSQL](https://www.postgresql.org/) - 强大的开源数据库
- [Patroni](https://patroni.readthedocs.io/) - PostgreSQL 高可用解决方案
- [Citus](https://docs.citusdata.com/) - PostgreSQL 分布式扩展
- [Go 语言](https://go.dev/) - 简单、可靠、高效的软件

---

## 📮 联系方式

- 项目主页：[GitHub](https://github.com/example/pg-deploy)
- 问题反馈：[Issues](https://github.com/example/pg-deploy/issues)
- 邮件：support@example.com

---

<div align="center">

**用 ❤️ 构建，为 PostgreSQL 社区**

**⭐ 如果这个项目对你有帮助，请给我们一个 Star！**

</div>
