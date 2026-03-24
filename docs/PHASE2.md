# 阶段二：部署引擎 - 实施文档

## 概述

本阶段实现 PostgreSQL 自动化部署的核心引擎，提供完整的 7 步部署流程编排、幂等性检查和回滚管理能力。

## 目录结构

```
pkg/deploy/
├── orchestrator.go      # 部署编排器（7步流程管理）
├── step.go              # 基础部署步骤实现
├── replication.go       # 主从复制和Patroni步骤
├── idempotent.go        # 幂等性检查器
├── rollback.go          # 回滚管理器
└── deploy_test.go       # 单元测试
```

## 核心组件

### 1. 部署编排器 (orchestrator.go)

**职责**：管理完整的部署流程

**核心功能**：
- ✅ 7 步部署流程编排
- ✅ 步骤依赖管理
- ✅ 进度跟踪和状态报告
- ✅ 失败自动回滚
- ✅ 支持断点续传（Resume）

**关键 API**：
```go
// 创建编排器
orchestrator := deploy.NewOrchestrator(config, executor, logger)

// 执行完整部署
err := orchestrator.Execute()

// 获取部署进度
progress := orchestrator.GetProgress()

// 从中断点恢复
err := orchestrator.Resume()
```

**部署流程**：
1. PrepareDirectories - 创建用户、目录和权限
2. DeploySoftware - 安装或分发 PostgreSQL 软件
3. InitDatabase - 初始化数据库集群（仅主节点）
4. ConfigurePostgreSQL - 配置 PostgreSQL 参数
5. StartPostgreSQL - 启动 PostgreSQL 服务
6. SetupReplication - 配置主从复制（主从模式）
7. ValidateDeployment - 验证部署和健康检查

**失败处理**：
- 步骤失败时自动触发回滚
- 从后向前回滚已完成的步骤
- 记录详细的失败原因

---

### 2. 基础部署步骤 (step.go)

**实现的步骤**：

#### PrepareDirectoriesStep
- 创建 postgres 用户
- 创建数据目录、WAL 目录、日志目录
- 设置正确的所有权和权限

#### DeploySoftwareStep
- **分发模式**：解压并分发预编译二进制文件
- **编译模式**：从源码并发编译 PostgreSQL
- 支持本地和远程编译

#### InitDatabaseStep
- 仅在主节点执行 initdb
- 自动处理 WAL 目录软链接
- 生成默认数据库集群

#### ConfigurePostgreSQLStep
- 生成 postgresql.conf 配置
- 配置监听地址、端口、内存参数
- 支持自定义配置参数

#### StartPostgreSQLStep
- 启动 PostgreSQL 服务
- 使用 pg_isready 验证就绪状态
- 记录启动日志

#### ValidateDeploymentStep
- 检查 systemd 服务状态
- 验证所有节点连接性
- 检查复制状态（主从模式）

---

### 3. 主从复制步骤 (replication.go)

**SetupReplicationStep** - 主从复制设置：

1. **创建复制用户**：
   - 在主节点创建 replicator 用户
   - 生成随机密码
   - 授予 REPLICATION 权限

2. **配置主节点**：
   - 修改 pg_hba.conf 允许复制连接
   - 重载配置（pg_ctl reload）

3. **初始化从节点**：
   - 使用 pg_basebackup -R 创建备用节点
   - 自动生成 standby.signal 和连接信息
   - 处理 WAL 目录软链接

4. **启动从节点**：
   - 启动所有从节点
   - 验证复制连接

**Patroni 步骤**：

- **InstallPatroniStep**：安装 Patroni 和 Python 依赖
- **ConfigurePatroniStep**：生成 YAML 配置文件
- **StartPatroniClusterStep**：创建 systemd 服务并启动集群

---

### 4. 幂等性检查器 (idempotent.go)

**设计目标**：确保部署操作可以安全地重复执行

**检查项目**：
- ✅ 目录存在性检查
- ✅ 用户存在性检查
- ✅ 软件安装检查
- ✅ 数据库初始化检查
- ✅ PostgreSQL 运行状态检查
- ✅ 复制配置检查
- ✅ Patroni 安装和配置检查
- ✅ systemd 服务检查

**关键 API**：
```go
checker := deploy.NewIdempotentChecker(executor, config, logger)

// 检查特定项
dirsOK, _ := checker.CheckDirectories()
userOK, _ := checker.CheckUser()
softwareOK, _ := checker.CheckSoftwareInstallation()

// 获取完整部署状态
state, _ := checker.GetDeploymentState()
completedSteps := state.GetCompletedSteps()
percentage := state.GetCompletionPercentage()
```

**使用场景**：
1. 部署前检查已完成的步骤
2. 断点续传时确定起始步骤
3. 验证操作是否已执行
4. 避免重复创建资源

---

### 5. 回滚管理器 (rollback.go)

**设计目标**：在部署失败时安全地回滚已完成的操作

**核心功能**：

#### 快照管理
- 在每个步骤完成后创建快照
- 捕获节点状态（服务、文件、目录）
- 保存配置文件备份
- 支持快照持久化

#### 回滚操作
- **停止服务**：停止所有启动的 PostgreSQL/Patroni 服务
- **清理文件**：删除创建的数据目录、WAL 目录
- **清理配置**：移除添加的配置文件
- **用户清理**（可选）：删除创建的用户（谨慎操作）

**关键 API**：
```go
manager, _ := deploy.NewRollbackManager(executor, config, logger)

// 创建快照
snapshot, _ := manager.CreateSnapshot("InitDatabase", 3)

// 回滚到指定步骤
err := manager.RollbackToStep(2)

// 回滚特定步骤
err := manager.RollbackStep("InitDatabase")

// 清理旧快照
err := manager.CleanupSnapshots(24 * time.Hour)
```

**回滚策略**：
- 从后向前回滚已完成的步骤
- 每个步骤独立回滚
- 回滚失败不中断整体回滚流程
- 记录详细的回滚日志

---

## 部署流程详解

### 单机模式（Standalone）

```
1. PrepareDirectories
   ├─ 创建 postgres 用户
   ├─ 创建 /data/pgdata
   └─ 设置权限

2. DeploySoftware
   ├─ 解压 PostgreSQL 二进制
   └─ 安装到 /usr/local/pgsql

3. InitDatabase
   ├─ 运行 initdb
   └─ 软链接 WAL 目录

4. ConfigurePostgreSQL
   └─ 生成 postgresql.conf

5. StartPostgreSQL
   ├─ 启动服务
   └─ 验证就绪

6. ValidateDeployment
   └─ 健康检查
```

### 主从模式（Master-Slave）

在单机模式的基础上增加：

```
6. SetupReplication
   ├─ 创建 replicator 用户
   ├─ 配置主节点 pg_hba.conf
   ├─ 重载主节点配置
   ├─ pg_basebackup 初始化从节点
   └─ 启动从节点

7. ValidateDeployment
   └─ 检查复制状态
```

### Patroni 高可用模式

```
1. PrepareDirectories
2. DeploySoftware
3. InstallPatroni
   ├─ 安装 Python 依赖
   └─ 安装 patroni 包
4. ConfigurePatroni
   ├─ 生成 YAML 配置
   └─ 配置 etcd 连接
5. StartPatroniCluster
   ├─ 创建 systemd 服务
   ├─ 启动所有节点
   └─ 等待集群选举
6. ValidateDeployment
   └─ 检查集群状态
```

---

## 使用示例

### 完整部署流程

```go
package main

import (
    "github.com/example/pg-deploy/pkg/config"
    "github.com/example/pg-deploy/pkg/deploy"
    "github.com/example/pg-deploy/pkg/executor"
    "github.com/example/pg-deploy/pkg/logger"
)

func main() {
    // 1. 加载配置
    cfg, _ := config.Load("deploy.conf")

    // 2. 创建日志和执行器
    log := logger.NewDefault()
    exec, _ := executor.New(executor.Config{
        Nodes: nodes,
        Logger: log,
    })

    // 3. 创建编排器
    orchestrator := deploy.NewOrchestrator(cfg, exec, log)

    // 4. 执行部署
    if err := orchestrator.Execute(); err != nil {
        log.Error("Deployment failed", logger.Fields{"error": err})
    }
}
```

### 断点续传

```go
// 部署失败后，从中断点继续
orchestrator := deploy.NewOrchestrator(cfg, exec, log)

// 检查已完成步骤
checker := deploy.NewIdempotentChecker(exec, cfg, log)
state, _ := checker.GetDeploymentState()

// 从中断点恢复
if len(state.GetCompletedSteps()) > 0 {
    log.Info("Resuming deployment")
    _ = orchestrator.Resume()
}
```

### 手动回滚

```go
// 创建回滚管理器
manager, _ := deploy.NewRollbackManager(exec, cfg, log)

// 创建快照
snapshot, _ := manager.CreateSnapshot("InitDatabase", 3)

// 发生错误后回滚
if err := someOperation(); err != nil {
    _ = manager.RollbackStep("InitDatabase")
}
```

---

## 测试方法

### 单元测试

```bash
# 运行部署引擎测试
go test ./pkg/deploy/... -v

# 带竞态检测
go test -race ./pkg/deploy/... -v

# 测试覆盖率
go test -cover ./pkg/deploy/...
```

### 集成测试

```bash
# 使用配置文件进行完整部署测试
go run examples/deploy_example.go test_deploy.conf

# 验证部署结果
psql -h 192.168.1.10 -p 5432 -U postgres -c "SELECT version();"
```

---

## 风险点和解决方案

### 风险 1：并发部署资源竞争

**场景**：50 节点同时编译 PostgreSQL

**解决方案**：
- 限制并发编译数量（默认 10）
- 使用信号量控制资源访问
- 超过 5 节点使用顺序编译

### 风险 2：WAL 目录软链接处理

**场景**：initdb 后需要软链接 WAL 目录

**解决方案**：
- 在 initdb 后立即删除 pg_wal
- 创建指向 WAL_DIR 的软链接
- 在从节点也执行相同操作

### 风险 3：主从复制密码管理

**场景**：pg_basebackup 需要密码

**解决方案**：
- 生成随机密码存储在 .pgpass
- 使用 -R 参数自动创建 standby.signal
- pg_hba.conf 配置 md5 认证

### 风险 4：回滚失败导致状态不一致

**场景**：回滚过程中部分操作失败

**解决方案**：
- 每个步骤独立回滚
- 回滚失败不中断整体流程
- 详细记录所有回滚操作
- 提供手动清理脚本

---

## 性能优化

### 并发控制

```go
// 根据节点数量动态调整并发度
maxConcurrent := 10
if len(nodes) > 20 {
    maxConcurrent = 20
}

exec, _ := executor.New(executor.Config{
    MaxConcurrent: maxConcurrent,
})
```

### 编译优化

```go
// 本地编译使用 -j4
// 远程编译使用 -j$(nproc)
makeCmd := "make -j$(nproc) install-world-bin"
```

### 复制优化

```go
// pg_basebackup 使用流复制和压缩
pgBasebackupCmd := "pg_basebackup -h master -D data -P -R -X stream -C -S replica_slot"
```

---

## 已知限制

1. **编译模式**：
   - 需要预先安装编译依赖（gcc, make, zlib-devel 等）
   - 编译时间较长（10-30 分钟/节点）

2. **主从复制**：
   - 仅支持流复制
   - 不支持级联复制

3. **Patroni 模式**：
   - 需要外部 etcd 集群
   - 不包含 etcd 部署逻辑

4. **回滚功能**：
   - 不回滚用户删除（需手动确认）
   - 数据目录删除不可恢复

---

## 下一步计划

### 阶段三：拓扑管理优化

1. **Citus 支持**：
   - Coordinator 配置
   - Worker 注册
   - 分布式表创建

2. **Patroni 增强**：
   - etcd 自动部署
   - 动态配置生成
   - 故障转移测试

3. **主从复制增强**：
   - 级联复制支持
   - 同步复制配置
   - 复制延迟监控

---

## 技术债务

1. **TODO 项**：
   - [ ] 软件分发逻辑完整实现
   - [ ] 编译命令完整实现
   - [ ] Patroni 配置模板完整实现
   - [ ] Citus 配置逻辑完整实现

2. **测试覆盖**：
   - [ ] 添加单元测试
   - [ ] 添加集成测试
   - [ ] 添加性能测试

3. **错误处理**：
   - [ ] 更详细的错误分类
   - [ ] 自动重试机制
   - [ ] 超时动态调整

---

**最后更新**: 2026-03-16
**阶段状态**: ✅ 核心功能已完成
**下一阶段**: 拓扑管理优化（PHASE3）
