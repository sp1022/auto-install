package deploy

import (
	"strings"
)

// compileCommand 生成编译命令
func (s *DeploySoftwareStep) compileCommand(ctx *Context) string {
	buildDir := "/tmp/pg_build"
	sourceFile := ctx.Config.PGSource
	installDir := ctx.Config.PGSoftDir
	configureOpts := ctx.Config.PGConfigureOpts

	// 如果用户没有指定 configure 选项，使用默认选项
	if configureOpts == "" {
		configureOpts = "--with-openssl --enable-thread-safety"
	}

	// 构建编译脚本（使用左侧对齐避免多余缩进）
	script := `#!/bin/bash
set -e

# 设置构建目录
export BUILD_DIR={{BUILD_DIR}}
export INSTALL_DIR={{INSTALL_DIR}}

# 清理旧构建目录和已安装的二进制（重新编译要求）
echo "[1/6] Cleaning up old build and installation..."
rm -rf $BUILD_DIR
rm -rf $INSTALL_DIR

# 重新创建构建目录
mkdir -p $BUILD_DIR
mkdir -p $INSTALL_DIR

# 检测并解压源码
SOURCE_FILE="{{SOURCE_FILE}}"
echo "[2/6] Source file: $SOURCE_FILE"
if [ ! -f "$SOURCE_FILE" ]; then
	echo "Error: Source file not found: $SOURCE_FILE"
	echo "Please ensure the source file exists on the target machine"
	exit 1
fi
if echo "$SOURCE_FILE" | grep -q '\.tar\.gz$' || echo "$SOURCE_FILE" | grep -q '\.tgz$'; then
	echo "[3/6] Extracting source tarball..."
	tar -xzf "$SOURCE_FILE" -C $BUILD_DIR --strip-components=1 || { echo "Error: Failed to extract tarball"; exit 1; }
else
	echo "[3/6] Using source directory..."
	if [ -d "$SOURCE_FILE" ]; then
		cp -r "$SOURCE_FILE"/* $BUILD_DIR/
	else
		echo "Error: Invalid source path"
		exit 1
	fi
fi

# 检查 configure 脚本是否存在
if [ ! -f "$BUILD_DIR/configure" ]; then
	echo "Error: configure script not found in $BUILD_DIR"
	echo "Contents of $BUILD_DIR:"
	ls -la $BUILD_DIR || true
	exit 1
fi

# 运行 configure
cd $BUILD_DIR
echo "[4/6] Configuring PostgreSQL..."
echo "Configure options: {{CONFIGURE_OPTS}}"
./configure --prefix={{INSTALL_DIR}} {{CONFIGURE_OPTS}} 2>&1 | tee /tmp/pg_configure.log || { echo "Error: configure failed, check /tmp/pg_configure.log"; exit 1; }

# 编译（使用所有可用 CPU 核心）
echo "[5/6] Compiling PostgreSQL..."
echo "Running: make world -j$(nproc)"
make world -j$(nproc) 2>&1 | tee /tmp/pg_make.log || { echo "Error: make world failed, check /tmp/pg_make.log"; exit 1; }

# 安装
echo "[6/6] Installing PostgreSQL..."
echo "Running: make install-world"
make install-world 2>&1 | tee /tmp/pg_make_install.log || { echo "Error: make install-world failed, check /tmp/pg_make_install.log"; exit 1; }

# 验证安装
if [ ! -x "{{INSTALL_DIR}}/bin/pg_config" ]; then
	echo "Error: Installation verification failed - pg_config not found"
	exit 1
fi

# 清理构建目录
cd /
rm -rf $BUILD_DIR

echo "========================================"
echo "PostgreSQL compilation and installation"
echo "completed successfully!"
echo "========================================"
`

	// 替换占位符
	script = strings.ReplaceAll(script, "{{BUILD_DIR}}", buildDir)
	script = strings.ReplaceAll(script, "{{INSTALL_DIR}}", installDir)
	script = strings.ReplaceAll(script, "{{SOURCE_FILE}}", sourceFile)
	script = strings.ReplaceAll(script, "{{CONFIGURE_OPTS}}", configureOpts)

	return script
}
