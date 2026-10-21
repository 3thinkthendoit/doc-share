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
# Chromium：HTML→PDF（复杂 CSS/彩图）；镜像体积会明显增大。
# Noto Sans SC（fontsource 简体子集）：Markdown PDF / Chromium 中文渲染 / 应急 htmlpdf。
# 应急：DOC_SHARE_HTML_PDF_LEGACY=1 才允许无 Chrome 时回退纯 Go htmlpdf（会叠字）。
ARG NOTO_SC_VER=5.2.5
RUN apk add --no-cache ca-certificates tzdata wget chromium nss freetype harfbuzz fontconfig \
    && mkdir -p /usr/share/fonts/noto-sc \
    && (wget -q -O /usr/share/fonts/noto-sc/NotoSansSC-Regular.ttf \
          "https://cdn.jsdelivr.net/fontsource/fonts/noto-sans-sc@${NOTO_SC_VER}/chinese-simplified-400-normal.ttf" \
        || wget -q -O /usr/share/fonts/noto-sc/NotoSansSC-Regular.ttf \
          "https://raw.githubusercontent.com/jsntn/webfonts/master/NotoSansSC-Regular.ttf") \
    && (wget -q -O /usr/share/fonts/noto-sc/NotoSansSC-Bold.ttf \
          "https://cdn.jsdelivr.net/fontsource/fonts/noto-sans-sc@${NOTO_SC_VER}/chinese-simplified-700-normal.ttf" \
        || cp /usr/share/fonts/noto-sc/NotoSansSC-Regular.ttf /usr/share/fonts/noto-sc/NotoSansSC-Bold.ttf) \
    && test -s /usr/share/fonts/noto-sc/NotoSansSC-Regular.ttf \
    && test -s /usr/share/fonts/noto-sc/NotoSansSC-Bold.ttf \
    && fc-cache -f \
    && CHROME="$(command -v chromium-browser || command -v chromium)" \
    && test -n "$CHROME" && test -x "$CHROME" \
    && ln -sf "$CHROME" /usr/bin/doc-share-chrome \
    && printf '%s\n' '<!DOCTYPE html><html><head><meta charset="utf-8"><title>smoke</title></head><body><p>ok</p></body></html>' > /tmp/chrome-smoke.html \
    && /usr/bin/doc-share-chrome --headless=new --no-sandbox --disable-gpu --disable-dev-shm-usage \
         --print-to-pdf=/tmp/chrome-smoke.pdf "file:///tmp/chrome-smoke.html" \
    && test -s /tmp/chrome-smoke.pdf \
    && rm -f /tmp/chrome-smoke.html /tmp/chrome-smoke.pdf \
    && apk del wget
ENV TZ=Asia/Shanghai
ENV DOC_SHARE_FONT_DIR=/usr/share/fonts/noto-sc
ENV DOC_SHARE_CHROME_PATH=/usr/bin/doc-share-chrome

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

# 日志：进程只写 stdout（含访问日志与 [boot]/[llm] 等），用 docker logs -f <容器> 查看；
# 勿再挂文件日志卷。连库卡住时会先看到 "[boot] 连接数据库…"，便于区分「没日志」和「卡在 MySQL」。
#
# 必需环境变量（config.yaml 中 session_secret 为裸 ${VAR} 占位，未设置会拒绝启动）：
#   DOC_SHARE_SESSION_SECRET      —— 随机长字符串（>=32 位），生产必填
#   DOC_SHARE_DB_DSN              —— MySQL DSN，如 user:pass@tcp(host:3306)/doc_share?charset=utf8mb4&parseTime=True&loc=Local
# 可选：
#   DOC_SHARE_PORT                —— 监听端口（默认 :8080）
#   DOC_SHARE_ADMIN_USER/_PASSWORD —— 首次启动创建的默认管理员
#   DOC_SHARE_DEV                 —— 前端热载，生产保持 false
#   DOC_SHARE_FONT_DIR            —— PDF 导出中文字体目录（镜像默认已含 Noto Sans SC）
#   DOC_SHARE_CHROME_PATH         —— HTML→PDF 用 Chromium 路径（镜像默认 /usr/bin/doc-share-chrome）
#   DOC_SHARE_HTML_PDF_LEGACY     —— 1/true 时无 Chrome 才回退 htmlpdf（排版差，仅应急）
#   DOC_SHARE_DB_LOG              —— true 时输出 GORM SQL（默认关闭）
ENTRYPOINT ["/app/main"]
CMD ["-config", "/etc/doc-share/config.yaml"]
