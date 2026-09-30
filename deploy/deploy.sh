#!/usr/bin/env bash
# 把当前已推送的 main 部署到公网服务器。在自己电脑的仓库根目录运行：
#
#   deploy/deploy.sh                       自动判断：上次部署之后改了哪个服务就部署哪个
#   deploy/deploy.sh server-go             只部署 Go 后端（可选 server-go、agent-py、web，可写多个）
#   deploy/deploy.sh rollback [服务...]    换回上一次部署前的镜像（不写服务名 = 三个都换）
#
# 做法（服务器只有 1.6 GiB 内存，不能在上面构建）：
#   1. 检查本地没有未提交、未推送的改动，服务器 git pull 到同一个提交
#   2. 在本机准备产物：交叉编译 server-go、构建前端 dist；agent-py 用服务器上拉下来的 app/ 目录
#   3. 在固定的基础镜像（:base）上叠一层，得到新的 :latest；旧的 :latest 记为 :prev 供回滚
#   4. 只重建改动的容器，等健康检查通过，再从公网访问一次
#   5. 任何一步失败都自动回滚到 :prev
#
# 依赖变了（agent-py 的 pyproject.toml / uv.lock、任一 Dockerfile、前端 nginx.conf）要完整构建镜像，见 deploy/DEPLOY.md「镜像」。
set -euo pipefail

HOST="${DEPLOY_HOST:-admin@47.84.60.190}"
# 带访问口令的短链接，跟随跳转后应返回 200
SMOKE_URL="${DEPLOY_SMOKE_URL:-https://yunfanteo.world/vparser}"
REMOTE_DIR="vparser"
WORK="/tmp/vparser-deploy"
DC="docker compose --profile app -f docker-compose.yml -f deploy/docker-compose.prod.yml"
ALL=(server-go agent-py web)

cd "$(dirname "$0")/.."
say() { printf '\n▶ %s\n' "$*"; }
die() { printf '\n✗ %s\n' "$*" >&2; exit 1; }
remote() { ssh -o ConnectTimeout=15 "$HOST" "cd ~/$REMOTE_DIR && $*"; }

rollback() {
  say "回滚到上一次部署前的镜像：$*"
  for svc in "$@"; do
    remote "docker image inspect vparser-$svc:prev >/dev/null 2>&1 && docker tag vparser-$svc:prev vparser-$svc:latest" \
      || echo "  $svc 没有 :prev 镜像，跳过"
  done
  remote "$DC up -d --no-build --no-deps $*" >/dev/null 2>&1 || true
}

wait_healthy() {
  for _ in $(seq 1 50); do
    bad=0
    for svc in "$@"; do
      [ "$(remote "docker inspect -f '{{.State.Health.Status}}' vparser-$svc-1" 2>/dev/null)" = healthy ] || bad=1
    done
    [ $bad = 0 ] && return 0
    sleep 3
  done
  return 1
}

smoke() {
  jar=$(mktemp)
  code=$(curl -sL -c "$jar" -o /dev/null -w '%{http_code}' --max-time 30 "$SMOKE_URL")
  rm -f "$jar"
  [ "$code" = 200 ] || { echo "  $SMOKE_URL 返回 $code"; return 1; }
}

if [ "${1:-}" = rollback ]; then
  shift
  [ $# -gt 0 ] || set -- "${ALL[@]}"
  rollback "$@"
  wait_healthy "$@" && smoke && say "已回滚，站点正常" && exit 0
  die "回滚后站点仍不正常，请登录服务器查看：$DC logs --tail 50 server-go"
fi

# ── 1. 本地和服务器对齐到同一个提交 ──────────────────────────────────────────────
[ -z "$(git status --porcelain)" ] || die "有未提交的改动，先提交"
git fetch -q origin
commit=$(git rev-parse HEAD)
[ "$commit" = "$(git rev-parse origin/main)" ] || die "本地 main 和 origin/main 不一致，先 git push"
[ -z "$(remote 'git status --porcelain --untracked-files=no')" ] \
  || die "服务器上的仓库有直接改过、没进 git 的文件。先确认这些改动是否要保留：ssh $HOST 'cd ~/$REMOTE_DIR && git diff'"

deployed=$(remote "cat .deployed 2>/dev/null || git rev-parse HEAD")
say "上次部署 ${deployed:0:7} → 这次 ${commit:0:7}"

targets=("$@")
if git cat-file -e "$deployed^{commit}" 2>/dev/null; then
  changed=$(git diff --name-only "$deployed" "$commit")
else
  changed=$(git ls-files)
fi
if [ ${#targets[@]} -eq 0 ]; then
  grep -qE '^(server-go|proto)/' <<<"$changed" && targets+=(server-go)
  grep -qE '^(agent-py|proto)/' <<<"$changed" && targets+=(agent-py)
  grep -q '^client/' <<<"$changed" && targets+=(web)
fi
blockers=$(grep -E '^(agent-py/(pyproject\.toml|uv\.lock|Dockerfile)|server-go/Dockerfile|client/(Dockerfile|nginx\.conf))$' <<<"$changed" || true)
[ -z "$blockers" ] || die "这些文件变了，叠一层不够，需要完整构建镜像（见 deploy/DEPLOY.md「镜像」）：
$blockers"

remote "git pull -q --ff-only"
[ "$(remote 'git rev-parse HEAD')" = "$commit" ] || die "服务器 git pull 后不是 ${commit:0:7}"

if [ ${#targets[@]} -eq 0 ]; then
  remote "echo $commit > .deployed"
  say "三个服务都没有改动，无需重建容器（compose 配置变了的话：ssh $HOST 'cd ~/$REMOTE_DIR && $DC up -d --no-build'）"
  exit 0
fi
say "要部署：${targets[*]}"

# ── 2–3. 准备产物，叠一层，生成新镜像 ───────────────────────────────────────────
remote "rm -rf $WORK && mkdir -p $WORK/server-go $WORK/web"
local_tmp=$(mktemp -d); trap 'rm -rf "$local_tmp"' EXIT
for svc in "${targets[@]}"; do
  remote "docker image inspect vparser-$svc:base >/dev/null 2>&1 || docker tag vparser-$svc:latest vparser-$svc:base"
  case "$svc" in
    server-go)
      say "本机交叉编译 server-go（linux/amd64）"
      (cd server-go && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o "$local_tmp/server" ./cmd/server)
      rsync -a "$local_tmp/server" "$HOST:$WORK/server-go/server"
      remote "printf 'FROM vparser-server-go:base\nCOPY server /usr/local/bin/server\n' > $WORK/server-go/Dockerfile \
              && docker build -q -t vparser-server-go:new $WORK/server-go >/dev/null 2>&1"
      ;;
    web)
      say "本机构建前端"
      [ -d client/node_modules ] || npm --prefix client ci --silent
      npm --prefix client run build --silent >/dev/null
      rsync -a --delete client/dist/ "$HOST:$WORK/web/dist/"
      remote "printf 'FROM vparser-web:base\nRUN rm -rf /usr/share/nginx/html/*\nCOPY dist/ /usr/share/nginx/html/\n' > $WORK/web/Dockerfile \
              && docker build -q -t vparser-web:new $WORK/web >/dev/null 2>&1"
      ;;
    agent-py)
      say "用服务器上的代码叠出 agent-py 镜像"
      remote "printf 'FROM vparser-agent-py:base\nUSER root\nRUN rm -rf /srv/agent/app\nCOPY app ./app\nUSER agent\n' > $WORK/Dockerfile.agent \
              && docker build -q -t vparser-agent-py:new -f $WORK/Dockerfile.agent agent-py >/dev/null 2>&1"
      ;;
    *) die "不认识的部署目标：$svc（可选 ${ALL[*]}）" ;;
  esac
done

# ── 4. 换镜像、重建容器、检查 ───────────────────────────────────────────────────
for svc in "${targets[@]}"; do
  remote "docker tag vparser-$svc:latest vparser-$svc:prev && docker tag vparser-$svc:new vparser-$svc:latest && docker rmi vparser-$svc:new >/dev/null"
done
say "重建容器：${targets[*]}"
# 从这里开始线上已经换成新镜像：后面任何一步失败都走回滚，不能直接退出
started=ok
remote "$DC up -d --no-build --no-deps ${targets[*]}" >/dev/null 2>&1 || started=failed

if [ "$started" = ok ] && wait_healthy "${targets[@]}" && smoke; then
  remote "echo $commit > .deployed; docker image prune -f >/dev/null; rm -rf $WORK"
  say "部署完成：${commit:0:7} 已上线，$SMOKE_URL 正常。回滚用 deploy/deploy.sh rollback"
else
  echo "✗ 新版本没有通过检查，自动回滚" >&2
  rollback "${targets[@]}"
  wait_healthy "${targets[@]}" && smoke && die "已回滚到旧版本，站点正常。新版本的日志：ssh $HOST 'cd ~/$REMOTE_DIR && $DC logs --tail 80 ${targets[*]}'"
  die "回滚后站点仍不正常，请立即登录服务器检查"
fi
