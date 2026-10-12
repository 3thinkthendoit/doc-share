# 自打包 Elasticsearch 镜像，HTTP 对外端口改为 8080
#
# 构建：  docker build --build-arg ES_VERSION=8.15.0 -f Dockerfile.es -t my-es .
# 运行：  docker run -d -p 8080:8080 -e ELASTIC_PASSWORD=xxx my-es
#
# ES 默认 HTTP 端口为 9200，本镜像通过覆盖 http.port 配置将其改为 8080，
# 适用于需要统一使用非标准端口的场景。
#
# 写入方式：用 sed 替换 —— ES 8.x 官方 elasticsearch.yml 中 http.port 是范围格式：
#     http.port: [9200, 9299]
# 正则需匹配此格式，否则 sed 静默失败，ES 仍监听 9200。

ARG ES_VERSION=8.15.0
FROM docker.elastic.co/elasticsearch/elasticsearch:${ES_VERSION}

EXPOSE 8080

# 替换 http.port（支持 9200 / [9200, 9299] 两种格式）
# 单节点模式也顺带写进去
RUN sed -i \
    -e 's|^http.port:.*|http.port: [8080]|' \
    config/elasticsearch.yml \
 && echo "discovery.type: single-node" >> config/elasticsearch.yml

# 健康检查
HEALTHCHECK --interval=30s --timeout=10s --start-period=60s \
  CMD curl -f http://localhost:8080/_cluster/health || exit 1
