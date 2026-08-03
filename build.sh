#!/bin/bash
# build.sh

set -euo pipefail

BUILD_STARTED_AT=$SECONDS

format_duration() {
	local elapsed="$1"
	local minutes=$((elapsed / 60))
	local seconds=$((elapsed % 60))

	if [ "$minutes" -gt 0 ]; then
		printf '%dm%02ds' "$minutes" "$seconds"
	else
		printf '%ds' "$seconds"
	fi
}

# project.json 维护稳定项目身份；PACKAGE_NAME 只控制本次构建产物名。
PROJECT_BINARY=$(go run ./tools/projectctl field binaryName)
VERSION=$(go run ./tools/projectctl field version)

branch_package_name() {
	local branch_name="$1"
	local semantic_name="$branch_name"

	semantic_name="${semantic_name#refs/heads/}"
	semantic_name="${semantic_name#codex/}"
	case "$semantic_name" in
		feature/*|feat/*|fix/*|bugfix/*|hotfix/*|chore/*|release/*)
			semantic_name="${semantic_name#*/}"
			;;
	esac

	printf '%s' "$semantic_name" \
		| tr '[:upper:]' '[:lower:]' \
		| sed -E 's/[^a-z0-9._-]+/-/g; s/^[._-]+//; s/[._-]+$//'
}

repository_package_name() {
	local remote_url
	local repository_name

	remote_url=$(git remote get-url origin 2>/dev/null || true)
	remote_url="${remote_url%/}"
	repository_name="${remote_url##*/}"
	repository_name="${repository_name%.git}"

	branch_package_name "$repository_name"
}

REQUESTED_PACKAGE_NAME="${PACKAGE_NAME:-}"
CURRENT_BRANCH=$(git branch --show-current 2>/dev/null || true)

if [ -n "$REQUESTED_PACKAGE_NAME" ]; then
	if [[ ! "$REQUESTED_PACKAGE_NAME" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]]; then
		echo "PACKAGE_NAME 只能包含字母、数字、点、下划线和短横线，并且必须以字母或数字开头"
		exit 1
	fi
	APP_NAME="$REQUESTED_PACKAGE_NAME"
	NAME_SOURCE="PACKAGE_NAME"
elif [ "$CURRENT_BRANCH" = "main" ]; then
	APP_NAME=$(repository_package_name)
	if [ -z "$APP_NAME" ]; then
		APP_NAME="$PROJECT_BINARY"
		NAME_SOURCE="project.json（无法读取 origin 仓库名）"
	else
		NAME_SOURCE="origin 仓库"
	fi
elif [ -n "$CURRENT_BRANCH" ]; then
	APP_NAME=$(branch_package_name "$CURRENT_BRANCH")
	if [ -z "$APP_NAME" ]; then
		APP_NAME="$PROJECT_BINARY"
		NAME_SOURCE="project.json（分支名无法生成可移植文件名）"
	else
		NAME_SOURCE="分支 $CURRENT_BRANCH"
	fi
else
	APP_NAME="$PROJECT_BINARY"
	NAME_SOURCE="project.json（detached HEAD）"
fi

if [ -z "$VERSION" ]; then
	echo "无法从 project.json 读取版本号"
	exit 1
fi

echo "构建产物名称：${APP_NAME}（来源：${NAME_SOURCE}）"

frontend_build_log=$(mktemp "${TMPDIR:-/tmp}/simple-openai-gateway-frontend-build.XXXXXX")
cleanup() {
	rm -f "$frontend_build_log"
}
trap cleanup EXIT

frontend_started_at=$SECONDS
if ! pnpm --dir frontend build >"$frontend_build_log" 2>&1; then
	echo "前端构建失败，详细日志：$frontend_build_log"
	trap - EXIT
	exit 1
fi
echo "✅ 前端构建完成，耗时：$(format_duration $((SECONDS - frontend_started_at)))"

# 清理之前的构建
rm -rf build
mkdir -p build

build_target() {
	local goos="$1"
	local goarch="$2"
	local output="$3"
	CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -buildvcs=false -o "$output" .
}

echo "+ Linux！"
build_target linux amd64 "build/${APP_NAME}-linux-x86-${VERSION}"
build_target linux arm64 "build/${APP_NAME}-linux-arm64-${VERSION}"

echo "+ Windows！"
build_target windows amd64 "build/${APP_NAME}-windows-x86-${VERSION}.exe"
build_target windows arm64 "build/${APP_NAME}-windows-arm64-${VERSION}.exe"

echo "+ macOS！"
build_target darwin amd64 "build/${APP_NAME}-darwin-x86-${VERSION}"
build_target darwin arm64 "build/${APP_NAME}-darwin-arm64-${VERSION}"

echo "✅ 构建完成，耗时：$(format_duration $((SECONDS - BUILD_STARTED_AT)))"
ls -lh build/
