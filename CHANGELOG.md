# PostgreSQL 部署工具 - 更新说明

## 最新更新 (2025-03-15)

### 1. 回滚 macOS 兼容性代码
- 移除所有 macOS 相关的兼容性代码
- 专注于 Debian/Linux 系统支持
- 简化代码，提高可维护性

### 2. Patroni 模式独立配置
**新增功能:**
- 创建独立的 `examples/patroni/patroni.conf` 配置文件
- 支持配置化的 Patroni 模板
- 添加 `PatroniConfig` 结构体
- 实现 `loadPatroniConfig()` 函数
- 实现 `getDefaultPatroniConfig()` 函数

**配置文件结构:**
```yaml
# etcd 配置
etcd_hosts: "127.0.0.1:2379"
etcd_protocol: "http"

# PostgreSQL 参数
pg_parameters:
  max_connections: "200"
  shared_buffers: "128MB"

# Citus 配置
enable_citus: true
citus_group: 0
citus_database: "postgres"

# 更多配置...
```

### 3. 配置文件示例
**新增文件:**
- `examples/patroni/patroni.conf` - Patroni 配置文件
- `examples/patroni/patroni.conf.example` - Patroni 配置示例
- `examples/patroni/deploy-patroni.conf` - Patroni 模式部署配置示例
- `docs/USER_MANUAL.md` - 当前使用手册

### 4. 测试工具
**新增内容:**
- `scripts/test-integration.sh` - 集成测试脚本

### 5. 代码结构优化
**新增类型:**
```go
type PatroniConfig struct {
    EtcdHosts              string
    EtcdProtocol           string
    RestAPIPort            int
    PGParameters           map[string]string
    EnableCitus            bool
    CitusGroup             int
    CitusDatabase          string
    // ... 更多字段
}
```

**新增全局变量:**
```go
var (
    PatroniConfFile string
    PatroniCfg      *PatroniConfig
)
```

### 6. configPatroni() 函数重构
- 使用配置文件生成 Patroni YAML
- 支持自定义 PostgreSQL 参数
- 支持自定义 pg_hba 规则
- 支持自定义 DCS 参数
- 支持 Citus 扩展配置

## 使用方法

### 主从模式部署
```bash
# 1. 编辑 deploy.conf
vim deploy.conf

# 2. 运行部署
./pg-deploy
# 选择: 1) 全新部署
```

### Patroni 模式部署
```bash
# 1. 编辑 examples/patroni/deploy-patroni.conf 和 Patroni 参数模板
vim examples/patroni/deploy-patroni.conf
vim examples/patroni/patroni.conf

# 2. 运行部署
./pg-deploy
# 选择: 1) 全新部署
```

### 运行验证
```bash
# 运行集成测试
./scripts/test-integration.sh
```

## 配置文件格式

### deploy.conf
```
ssh_user: root
deploy_mode: master-slave  # 或 patroni, single, multi-group
build_mode: distribute     # 或 compile
pg_source: /path/to/postgresql-17.9.tar.gz
pg_soft_dir: /usr/local/pgsql
group_0: 0|groupname|role|ip:port:data_dir:wal_dir:pglog_dir:is_master,...
```

### examples/patroni/patroni.conf
```yaml
# etcd 配置
etcd_hosts: "host1:2379,host2:2379,host3:2379"
etcd_protocol: "http"

# PostgreSQL 参数
pg_parameters:
  max_connections: "200"
  shared_buffers: "128MB"

# Citus 配置
enable_citus: true
citus_group: 0
citus_database: "postgres"
```

## 功能特性

### 已实现
- [x] 单机模式部署
- [x] 主从模式部署
- [x] Patroni 高可用模式
- [x] 多组模式 (Citus)
- [x] 源码编译模式
- [x] 二进制分发模式
- [x] WAL 目录独立配置
- [x] pglog 目录独立配置
- [x] 健康检查功能
- [x] 环境销毁功能
- [x] 配置文件管理
- [x] 日志记录
- [x] SSH 密钥和密码认证
- [x] 本地节点检测优化
- [x] Patroni 独立配置

### 待实现
- [ ] 备份恢复功能
- [ ] 配置版本管理
- [ ] 滚动升级支持
- [ ] 监控告警集成
- [ ] Web 管理界面
- [ ] 多版本 PostgreSQL 支持
- [ ] 配置验证功能
- [ ] 更详细的错误处理

## 系统要求

### 开发环境
- Go 1.23+
- macOS 或 Linux

### 生产环境
- Debian 10+ 或 Ubuntu 18.04+
- root 权限
- systemd 支持
- SSH 访问

### 依赖软件
- gcc, make
- zlib-devel, libssl-devel
- sshpass (可选，用于密码认证)

## 注意事项

1. **仅支持 Linux 系统** - 所有 systemd 相关功能
2. **需要 root 权限** - 用户创建、目录创建、服务管理
3. **SSH 访问** - 远程部署需要配置 SSH
4. **etcd 依赖** - Patroni 模式需要预先安装 etcd
5. **配置文件编码** - 必须是 UTF-8 编码

## 故障排除

### 常见问题
1. **编译失败** - 检查依赖软件是否安装
2. **SSH 连接失败** - 检查密钥或密码配置
3. **服务启动失败** - 查看 systemd 日志
4. **复制配置失败** - 检查 pg_hba.conf 和网络

### 调试方法
```bash
# 查看部署日志
tail -f deploy-*.log

# 查看 PostgreSQL 日志
tail -f /data/pglog/postgresql-*.log

# 查看 systemd 日志
journalctl -u postgresql-groupname -f

# 健康检查
./pg-deploy
# 选择: 2) 健康检查
```

## 贡献指南

1. Fork 项目
2. 创建功能分支
3. 提交更改
4. 推送到分支
5. 创建 Pull Request

## 许可证

本项目采用 MIT 许可证

## 联系方式

如有问题或建议，请提交 Issue
