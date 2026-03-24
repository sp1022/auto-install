#!/bin/bash

# 准备 PostgreSQL 分发包脚本
# 用于在已编译 PostgreSQL 的机���上打包二进制文件

set -e

echo "=========================================="
echo "  PostgreSQL 分发包准备工具"
echo "=========================================="
echo ""

# 检查是否已安装 PostgreSQL
if [ ! -d "/usr/local/pgsql" ]; then
    echo "❌ 错误: 未找到 PostgreSQL 安装目录: /usr/local/pgsql"
    echo ""
    echo "请先编译并安装 PostgreSQL，或指定安装目录"
    exit 1
fi

PG_DIR="/usr/local/pgsql"
echo "PostgreSQL 安装目录: $PG_DIR"
ls -lh "$PG_DIR"
echo ""

# 获取 PostgreSQL 版本
if [ -f "$PG_DIR/bin/postgres" ]; then
    VERSION=$("$PG_DIR/bin/postgres" --version | awk '{print $3}' | cut -d. -f1-2)
    echo "检测到 PostgreSQL 版本: $VERSION"
else
    echo "⚠️  警告: 无法检测 PostgreSQL 版本"
    VERSION="unknown"
fi
echo ""

# 设置输出文件名
OUTPUT_FILE="${1:-/tmp/pgsql-${VERSION}-bin.tar.gz}"

echo "准备分发包..."
echo "输出文件: $OUTPUT_FILE"
echo ""

# 检查输出目录
OUTPUT_DIR=$(dirname "$OUTPUT_FILE")
if [ ! -d "$OUTPUT_DIR" ]; then
    echo "创建输出目录: $OUTPUT_DIR"
    mkdir -p "$OUTPUT_DIR"
fi

# 删除旧文件（如果存在）
if [ -f "$OUTPUT_FILE" ]; then
    echo "删除旧文件: $OUTPUT_FILE"
    rm -f "$OUTPUT_FILE"
fi

# 打包
echo "开始打包..."
cd "$PG_DIR"
tar -czf "$OUTPUT_FILE" .
echo ""

# 验证
if [ -f "$OUTPUT_FILE" ]; then
    echo "✅ 打包成功!"
    ls -lh "$OUTPUT_FILE" | awk '{print "文件大小: " $5}'
    echo ""

    echo "文件内容预览:"
    tar -tzf "$OUTPUT_FILE" | head -20
    echo "   ..."
    echo ""

    # 验证关键文件
    echo "验证关键文件..."
    if tar -tzf "$OUTPUT_FILE" | grep -q "^bin/postgres$"; then
        echo "  ✅ bin/postgres"
    else
        echo "  ❌ 缺少 bin/postgres"
    fi

    if tar -tzf "$OUTPUT_FILE" | grep -q "^bin/psql$"; then
        echo "  ✅ bin/psql"
    else
        echo "  ❌ 缺少 bin/psql"
    fi

    if tar -tzf "$OUTPUT_FILE" | grep -q "^bin/pg_ctl$"; then
        echo "  ✅ bin/pg_ctl"
    else
        echo "  ❌ 缺少 bin/pg_ctl"
    fi

    if tar -tzf "$OUTPUT_FILE" | grep -q "^lib/"; then
        LIB_COUNT=$(tar -tzf "$OUTPUT_FILE" | grep "^lib/" | wc -l)
        echo "  ✅ lib/ 目录 ($LIB_COUNT 个文件)"
    else
        echo "  ❌ 缺少 lib/ 目录"
    fi

    if tar -tzf "$OUTPUT_FILE" | grep -q "^share/"; then
        SHARE_COUNT=$(tar -tzf "$OUTPUT_FILE" | grep "^share/" | wc -l)
        echo "  ✅ share/ 目录 ($SHARE_COUNT 个文件)"
    else
        echo "  ❌ 缺少 share/ 目录"
    fi

    echo ""
    echo "=========================================="
    echo "  分发包准备完成"
    echo "=========================================="
    echo ""
    echo "分发包路径: $OUTPUT_FILE"
    echo ""
    echo "使用方法:"
    echo "  1. 将分发包复制到部署脚本目录"
    echo "  2. 在 deploy.conf 中配置:"
    echo "     build_mode: distribute"
    echo "     distribute_file: $OUTPUT_FILE"
    echo "  3. 运行部署: ./pg-deploy"
    echo ""
else
    echo "❌ 打包失败!"
    exit 1
fi
