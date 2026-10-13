# 自打包 Elasticsearch 镜像，HTTP 对外端口改为 8080，预装 IK 中文分词插件，并默认开启安全认证
#
# 构建：  docker build --build-arg ES_VERSION=8.15.0 -f Dockerfile.es -t my-es .
# 运行：  docker run -d -p 8080:8080 -e ELASTIC_PASSWORD="你的强密码" \
#                -e ES_JAVA_OPTS="-Xms2g -Xmx2g" my-es
#         ⚠ 本镜像默认开启 xpack.security：不设 ELASTIC_PASSWORD 将无法完成健康检查。
#         应用侧必须同时配 DOC_SHARE_ES_USERNAME=elastic + DOC_SHARE_ES_PASSWORD=<同上>，
#         否则 ES 请求会拿到 401，应用会静默降级为 MySQL 搜索。
#
# ES 默认 HTTP 端口为 9200，本镜像通过覆盖 http.port 配置将其改为 8080，
# 适用于需要统一使用非标准端口的场景。
#
# 【安全提醒】切勿将关闭认证的 ES 暴露到公网！本镜像现默认强制密码认证；
# 若仅用于完全可信的内网且确实想免认证，可自行改回 xpack.security.enabled: false，但风险自担。
#
# 写入方式：用 sed 替换 —— ES 8.x 官方 elasticsearch.yml 中 http.port 是范围格式：
#     http.port: [9200, 9299]
# 正则需匹配此格式，否则 sed 静默失败，ES 仍监听 9200。

ARG ES_VERSION=8.15.0
FROM docker.elastic.co/elasticsearch/elasticsearch:${ES_VERSION}

# FROM 之前声明的 ARG 不会自动传入构建阶段，需在阶段内重新声明才能在 RUN 中引用
ARG ES_VERSION=8.15.0

EXPOSE 8080

# 安装 IK 中文分词插件（analysis-ik）：插件版本必须与 ES 版本完全一致，故用 ${ES_VERSION}。
# 官方推荐从 infini.cloud 拉取对应版本；--batch 免交互确认权限。
# 容错：该插件是「增强」而非「必需」——若源站不可达/该版本未发布，不让 docker build 失败
# （否则 mirror-es.yml 会被连带卡死）；应用侧（internal/search）会自动探测：装了用
# ik_max_word/ik_smart 中文分词，未装回退标准分析器，故安装失败仅降低中文分词质量，不影响 ES 可用。
RUN bin/elasticsearch-plugin install --batch \
    https://get.infini.cloud/elasticsearch/analysis-ik/${ES_VERSION} \
 || echo "WARN: analysis-ik ${ES_VERSION} 安装失败，ES 将回退标准分析器（中文按单字切分）"

# JVM 堆不在镜像里烤死：堆是运行期/环境相关参数，启动时用 -e ES_JAVA_OPTS 传即可，
# 改堆无需重新打包。例：docker run -e ES_JAVA_OPTS="-Xms2g -Xmx2g" ...（不传则 ES 8.x 自动按容器内存算堆）。

# 替换 http.port（支持 9200 / [9200, 9299] 两种格式）；单节点模式一并写入。
# 安全：默认开启 xpack.security（强制密码认证），但 HTTP 传输层不加密
# （xpack.security.http.ssl.enabled: false）——HTTPS 由上层反代/云平台终结；
# transport.ssl 也关闭（单节点无节点间通信）。elastic 超级用户的密码由运行期
# ELASTIC_PASSWORD 环境变量引导设置（仅首次启动、安全索引为空时生效）。
RUN sed -i \
    -e 's|^http.port:.*|http.port: 8080|' \
    config/elasticsearch.yml \
 && grep -q "^http.port:" config/elasticsearch.yml \
 || echo "http.port: 8080" >> config/elasticsearch.yml \
 && echo "discovery.type: single-node" >> config/elasticsearch.yml \
 && echo "xpack.security.enabled: true" >> config/elasticsearch.yml \
 && echo "xpack.security.http.ssl.enabled: false" >> config/elasticsearch.yml \
 && echo "xpack.security.transport.ssl.enabled: false" >> config/elasticsearch.yml

# 健康检查：开启认证后需带 elastic 凭据，否则 401 会被判为不健康
HEALTHCHECK --interval=30s --timeout=10s --start-period=60s \
  CMD curl -f -u "elastic:${ELASTIC_PASSWORD}" http://localhost:8080/_cluster/health || exit 1
