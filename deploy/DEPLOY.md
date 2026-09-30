# 公网部署（与 GoEuroOps 共用一台小服务器）

演示服务器只有 2 vCPU / 2 GiB，Vparser 和 GoEuroOps 共用，HTTPS 和访问口令由 GoEuroOps 的 Caddy 统一提供。
`deploy/docker-compose.prod.yml` 在默认配置上叠加内存调优、CPU 限额和网络接入；本地开发不叠加它，行为不变。

| 地址 | 内容 | 访问 |
|---|---|---|
| `https://vparser.<SITE_ADDRESS>/?key=<访问密钥>` | 工作台 | 专属访问链接（写入 Cookie） |
| `https://jaeger-vparser.<SITE_ADDRESS>` | 链路追踪 | 口令 |
| `https://s3-vparser.<SITE_ADDRESS>` | 视频播放（预签名链接） | 签名即授权 |

## 前提

GoEuroOps 已按它的 `deploy/DEPLOY.md` 部署好（提供 Caddy 和 `goeuroops_default` 网络）。

## 镜像

小机器上不要构建：在开发机上按服务器架构构建，再传过去。

```bash
for s in server-go agent-py; do docker buildx build --platform linux/amd64 -t vparser-$s:latest --load ./$s; done
docker buildx build --platform linux/amd64 -t vparser-web:latest --load ./client
docker save vparser-server-go vparser-agent-py vparser-web | gzip | ssh <服务器> 'gunzip | docker load'
```

## 日常更新

在自己电脑的仓库根目录运行（先提交并推送）：

```bash
deploy/deploy.sh                     # 上次部署之后改了哪个服务就部署哪个
deploy/deploy.sh server-go web       # 指定服务
deploy/deploy.sh rollback            # 换回上一次部署前的镜像
```

脚本在本机交叉编译 server-go、构建前端，在服务器上给基础镜像（`:base`）叠一层，只重建改动的容器，
等健康检查通过并从公网访问一次；不通过就自动回滚。依赖或 Dockerfile 变了时脚本会停下来，
这时按上面「镜像」一节完整构建并传上去，再执行 `docker tag vparser-<服务>:latest vparser-<服务>:base`。

## 配置与启动（服务器上）

```bash
git clone https://github.com/44sunsetsf/Vparser.git ~/vparser && cd ~/vparser
# .env：参照 .env.example 填写各项密码和 SILICONFLOW_API_KEY，另加
#   SITE_ADDRESS=<与 GoEuroOps 相同>
chmod 600 .env

# 站点配置：替换 VPARSER_GATE_KEY（随机字母数字），以及 Jaeger 的 VPARSER_USER / VPARSER_PASSWORD_HASH
docker run --rm caddy:2-alpine caddy hash-password --plaintext '<口令>'
cp deploy/vparser.caddy.example ~/goeuroops/deploy/sites/vparser.caddy   # 然后编辑

docker compose --profile app -f docker-compose.yml -f deploy/docker-compose.prod.yml up -d --no-build
docker exec goeuroops-caddy-1 caddy reload --config /etc/caddy/Caddyfile
```
