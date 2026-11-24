# PanStride

PanStride 是用 Go 编写的百度网盘命令行下载器。它支持按名称搜索个人网盘、按路径或文件 ID 下载，并通过 aria2c 并行传输文件的不同区段。下载中断后，可以从已保存的位置继续。

## 快速开始

从项目根目录构建。构建需要 Go 1.26.2 或更新版本，下载需要 PATH 中有 `aria2c`。

```sh
go build -o panstride .
./panstride --help
./panstride login
```

在浏览器打开终端给出的授权地址。百度页面会显示 **OpenList** 应用。授权后，将成功页的完整地址（包括 `#` 后的内容）粘贴回终端并回车。PanStride 会隐藏输入并保存令牌。

```sh
./panstride whoami
./panstride ls --ids "/资料"
./panstride search "课程"
./panstride download "/资料/课程.mp4" -o ./downloads
```

PanStride 默认每个文件最多使用 8 条连接。64 MiB 及以上的文件默认启用停滞恢复，连续 45 秒没有新数据时重取直链并重试一次。下载速度随账号权限、网络和百度下载服务器的响应变化。

## 文档

- [CLI 命令参考](docs/cli.md)涵盖参数、JSON 输出、续传与常见错误。
- [架构与开发](docs/architecture.md)介绍代码结构、下载流程和本地测试。
