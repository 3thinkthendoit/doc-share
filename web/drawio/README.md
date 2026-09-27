# drawio 编辑器嵌入目录（CI 构建期填充）

本目录用于在 **CI/Docker 构建期** 下载 [jgraph/drawio](https://github.com/jgraph/drawio)（Apache 2.0）
官方源码包中的 webapp 静态文件，随后经 `go:embed all:web/drawio` 编入 DocShare 二进制，
实现单容器、零外部依赖的 drawio 文档编辑。

- Dockerfile 构建阶段会执行下载（版本由 `DRAWIO_VERSION` 构建参数锁定），
  因此**仓库中不需要提交 drawio 源文件**，本 README 仅为 `go:embed` 的占位。
- 未下载时二进制内无 `index.html`，启动时探测不到 → drawio 文档功能降级为「未配置」提示，
  其余功能不受影响。
- 本地开发需要 drawio 时，可手动执行等价下载后以 `--dev` 模式运行
  （dev 模式直读本目录，不依赖重新编译）：

  ```sh
  curl -fsSL https://github.com/jgraph/drawio/archive/refs/tags/v24.7.5.tar.gz \
    | tar xz --strip-components=2 -C web/drawio drawio-24.7.5/src/main/webapp
  ```

如需指向外部已部署的 drawio 实例，配置 `drawio.editor_url` 即可，优先级高于内嵌。
