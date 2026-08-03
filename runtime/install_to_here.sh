#!/bin/bash

set -euo pipefail

SCRIPT_DIR=$(
	cd "$(dirname "${BASH_SOURCE[0]}")"
	pwd
)
PROJECT_ROOT=$(
	cd "$SCRIPT_DIR/.."
	pwd
)
BUILD_DIR="$PROJECT_ROOT/build"

echo "+ 在项目根目录执行 build.sh"
(
	cd "$PROJECT_ROOT"
	./build.sh
)

darwin_arm64_artifacts=("$BUILD_DIR"/*-darwin-arm64-*)
if [ ! -f "${darwin_arm64_artifacts[0]}" ]; then
	echo "未在 $BUILD_DIR 找到 Darwin ARM64 构建产物"
	exit 1
fi
if [ "${#darwin_arm64_artifacts[@]}" -ne 1 ]; then
	echo "在 $BUILD_DIR 找到多个 Darwin ARM64 构建产物，无法确定安装目标"
	printf '  %s\n' "${darwin_arm64_artifacts[@]}"
	exit 1
fi

artifact="${darwin_arm64_artifacts[0]}"
destination="$SCRIPT_DIR/$(basename "$artifact")"
cp "$artifact" "$destination"

echo "已安装 Darwin ARM64 产物：$destination"
