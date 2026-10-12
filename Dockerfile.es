# 自打包 Elasticsearch 镜像，HTTP 对外端口改为 8080
#
# 构建：  docker build -t my-es:8.15.0 -f Dockerfile.es .
# 运行：  docker run -d -p 8080:8080 -e ELASTIC_PASSWORD=xxx my-es:8.15.0
#
# ES 默认 HTTP 端口为 9200，本镜像通过覆盖 http.port 配置将其改为 8080，
# 适用于需要统一使用非标准端口的场景。

ARG ES_VERSION=8.15.0
FROM docker.elastic.co/elasticsearch/elasticsearch:${ES_VERSION}

# 修改 HTTP 端口为 8080：用 sed 显式替换默认值，避免 YAML 解析歧义
#（官方配置文件中默认有 http.port: 9200，>> 追加会被前面的值覆盖）
# 单节点模式：discovery.type 不是环境变量，只能通过配置文件设置
EXPOSE 8080
RUN sed -i 's/^http.port: 9200$/http.port: 8080/' config/elasticsearch.yml \
 && echo "discovery.type: single-node" >> config/elasticsearch.yml

# 健康检查（ES 8.x 默认开启安全认证时需加 -u elastic:$ES_PASSWORD）
HEALTHCHECK --interval=30s --timeout=10s --start-period=60s \
  CMD curl -f http://localhost:8080/_cluster/health || exit 1

# 如需关闭安全认证（内部测试环境），取消下面注释
# RUN echo "xpack.security.enabled: false" >> config/elasticsearch.yml
