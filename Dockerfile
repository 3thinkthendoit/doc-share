# ---- build stage：容器内从源码编译，无需本地预编译 ----
FROM golang:1.26-alpine AS builder

ENV GOPROXY=https://goproxy.cn,direct
WORKDIR /build

# 先拷贝依赖清单，利用构建缓存
COPY go.mod go.sum ./
RUN go mod download

# 拷贝源码并编译（web/ 模板与静态资源由 go:embed 编入二进制，纯 Go 依赖可关闭 CGO 静态编译）
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o main .

# ---- runtime stage ----
FROM alpine

# ca-certificates：外链图片/HTTPS 必需；tzdata：日志时间用东八区
RUN apk add --no-cache ca-certificates tzdata
ENV TZ=Asia/Shanghai

WORKDIR /app
COPY --from=builder /build/main /app/main

# 配置文件：仓库里的 config.yaml 全部为 ${VAR:-默认值} 占位符形式，不含真实密钥，
# 运行时通过环境变量注入（见下方变量说明）。
# 注意：若以后把真实密码写进 config.yaml，务必同步加入 .dockerignore 并改用挂载配置
COPY config.yaml /etc/doc-share/config.yaml

# 上传文件（图片/Logo）目录：建议挂卷持久化，容器重建不丢数据
RUN mkdir -p /app/uploads
ENV DOC_SHARE_UPLOAD_DIR=/app/uploads
VOLUME ["/app/uploads"]

EXPOSE 8080

# 必需环境变量（config.yaml 中 session_secret 为裸 ${VAR} 占位，未设置会拒绝启动）：
#   DOC_SHARE_SESSION_SECRET      —— 随机长字符串（>=32 位），生产必填
#   DOC_SHARE_DB_DSN              —— MySQL DSN，如 user:pass@tcp(host:3306)/doc_share?charset=utf8mb4&parseTime=True&loc=Local
# 可选：
#   DOC_SHARE_PORT                —— 监听端口（默认 :8080）
#   DOC_SHARE_ADMIN_USER/_PASSWORD —— 首次启动创建的默认管理员
#   DOC_SHARE_DEV                 —— 前端热载，生产保持 false
ENTRYPOINT ["/app/main"]
CMD ["-config", "/etc/doc-share/config.yaml"]
