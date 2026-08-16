# go-anydoc

[English](README.md) | 简体中文

[anydoc](https://github.com/firecrawl/anydoc) 的 Go 绑定——把 Word、PowerPoint、Excel、OpenDocument、RTF、EPUB、CSV 和文本型 PDF 转成 Markdown。

**无 cgo，无子进程，无系统依赖。** Rust 库编译成 WebAssembly 内嵌进包里，所以用了这个包的二进制是自包含的，Go 能交叉编译到哪它就能到哪：

```
CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build ./...
```

## 安装

```
go get github.com/xusenlin/go-anydoc
```

需要 Go 1.25 或更高版本，这是 wazy 的要求。

## 用法

```go
c, err := anydoc.New()
if err != nil {
    return err
}
defer c.Close(ctx)

md, err := c.Convert(ctx, docBytes, "docx")
```

格式提示传 `""` 表示从内容自动识别。CSV 没有文件签名，必须显式指定。

错误用 `errors.Is` 判断：

```go
switch {
case errors.Is(err, anydoc.ErrEncrypted):    // 有密码保护
case errors.Is(err, anydoc.ErrUnsupported):  // anydoc 不认识的格式
case errors.Is(err, anydoc.ErrMalformed):    // 格式认得出但内容损坏
}
```

## 设计取舍

**用解释器而不是优化编译器。** wazy 有两种执行引擎：一种在加载时把 wasm 翻译成宿主机原生机器码（跑得快，加载贵，只支持 amd64/arm64），另一种逐条解释执行（加载几乎免费，哪都能跑，跑得慢）。本包**默认**用解释器——一个要塞进别人二进制里的库，没法假定自己能在用户机器上预热编译缓存，没法假定宿主允许编译器需要的可执行内存页（macOS hardened runtime 和某些 seccomp 策略会直接拒绝），更没法假定目标平台是 amd64 或 arm64。需要另一头的取舍时用 `WithCompiler` 显式打开。

但**应用**是知道自己的数据目录在哪的，这会改变整个算式：`WithCompilationCache` 让编译器那笔成本变成一次性的 2.5 秒和 630 MB，此后每次启动都是 7 毫秒、36 MB——比解释器还便宜，转换还快两个数量级。下面的默认值是按「没有缓存」设的，因为库不能假定有。

代价是真实存在的，而且随文档体积放大，上生产前请拿自己的语料实测：

| | 解释器（默认） | 编译器 | 编译器 + 热缓存 |
|---|---|---|---|
| `New()`——每进程一次 | 101 ms | 2.5 s | **7 ms** |
| 1 KB docx | 2.4 ms | 0.51 ms | 0.51 ms |
| 解压后正文 5 MB 的 docx | 8.4 s | 0.18 s | 0.18 s |
| 7.5 MB PDF | 34.5 s | 0.77 s | 0.77 s |
| `New()` 之后的 RSS | 120 MB | 630 MB | **36 MB** |

第三列就是第二列在 `WithCompilationCache` 有目录可读之后的样子——执行方式完全相同，只是不用再付启动成本。转换耗时三列里第二第三完全一致，因为缓存改变的是机器码**怎么拿到**，不是它**是什么**。第二列只有每台机器的第一次会付。

常规办公文档落在第二行，几毫秒，没有优化价值。上兆字节的文档则比编译模式慢约 **45 倍**——如果你要处理这类文档，要么用 `WithMaxInputBytes` 加 context 超时把长尾兜住（取消会当场打断 guest），要么显式打开 `WithCompiler`。

**用 wazy，不用 wazero。** [wazy](https://github.com/samyfodil/wazy) 是一个由 wazero 衍生而来的纯 Go 运行时，它把力气花在了主导这类负载的内存访问路径上。同一份模块、同一份输入、同一台机器：

| 转换 | wazero v1.12.0 | wazy v0.1.3 | |
|---|---|---|---|
| 1 KB docx，编译器 | 0.73 ms | 0.51 ms | 1.4× |
| 正文 5 MB 的 docx，编译器 | 0.85 s | 0.18 s | **4.7×** |
| 7.5 MB PDF，编译器 | 3.63 s | 0.77 s | **4.7×** |
| 1 KB docx，解释器 | 2.55 ms | 2.39 ms | 1.1× |
| 正文 5 MB 的 docx，解释器 | 10.9 s | 8.4 s | 1.3× |
| 7.5 MB PDF，解释器 | 43.8 s | 34.5 s | 1.3× |

优势集中在长时间计算，而且计算越长差距越大。另一半是内存分配：解释执行那份 PDF，wazy 是 472 次分配、100 MB，wazero 是 5800 万次、1.5 GB。

移植只改了一处 import。API、内嵌模块、退出码 ABI 全都没变，输出逐字节一致，交叉编译到 riscv64、ppc64le、386、s390x 同样正常。

把取舍摊开说：wazy 只有一个月大，单一作者，且明确声明不作 API 稳定性承诺；wazero 成熟、部署广泛、背后有公司。本包选了新的那个，因为处理"长到值得在意 4.7 倍"的文档正是它的全部工作——也因为退回去同样只是那一行。

<sub>本页每个数字都出自 `bench_test.go`，可以自己核验而不必相信：`go test -run '^$' -bench . -benchtime 3x`，PDF 那几行加 `ANYDOC_BENCH_PDF=big.pdf`。实测环境：Apple M5 Pro（18 核），48 GB，macOS 26.5，Go 1.26.1，`CGO_ENABLED=0`，`anydoc.wasm` 6,833,738 字节（anydoc 0.1.9）。5 MB 那一行是 2.5 万行表格，zip 后只有 42 KB——真正决定耗时的是解压后的正文体积——并且需要 `WithMemoryLimitPages(1280)`，高于默认值。</sub>

**每份文档一个全新 guest。** 每次 `Convert` 都新建独立的线性内存，所以一份大文档不会让内存被永久占住，一份畸形文档也不会把状态泄漏给下一次调用。真正昂贵的编译只在 `New` 里做一次。

**用 command 模块而不是导出函数。** guest 读 stdin 写 stdout，Go 侧完全不用碰线性内存、指针和 UTF-8 边界，错误类型由退出码携带。见 `rust/src/main.rs`——那里的退出码表和 `errors.go` 是一份跨两种语言的契约。

**沙箱。** 只挂 WASI 的 stdio 和 `random_get`（`HashMap` 播种要用）。不给文件系统，不给时钟，不给套接字。

## 控制体积

默认内嵌模块，`go get` 完 `New()` 就能跑。想把载荷单独分发的：

```
go build -tags anydoc_nowasm    # 省下 6.89 MB
```

此时 `embeddedWASM` 为 nil，`New` 要求必须传 `WithWASM(r)` 或 `WithWASMBytes(b)`。适用于容器分层、Serverless 部署包大小限制、锁定另一个 anydoc 构建，或者禁止二进制内嵌不可追溯 blob 的合规环境。

省下的是 6.83 MB 的模块本身，再加上约 56 KB 的 `embed` 机制开销。给个体感：`examples/convert` 正常编译 14.6 MB，加上这个 tag 是 7.7 MB。

注意 build tag 只影响编译产物，不影响 `go get`——模块文件在 Go module 里躺着，两种情况都要下载。

## 参数调节

```go
anydoc.New(
    anydoc.WithConcurrency(4),        // 同时运行的 guest 数，控制峰值内存的主要手段
    anydoc.WithMemoryLimitPages(1024), // 单个 guest 64 MiB
    anydoc.WithMaxInputBytes(64<<20),
    anydoc.WithCompiler(),            // 用启动成本换吞吐，见下
    anydoc.WithCompilationCache(dir), // 让那笔成本只付一次，见下
)
```

### `WithCompiler`

把模块编译成原生机器码而不是解释执行，效果就是把上面那张表从左列变成右列。

这笔成本**只在 `New` 里付一次**，不是每份文档都付——`Convert` 只做实例化，用的是已经编好的模块。所以它适合长期运行、复用同一个 `Converter` 的进程；对于「转一份小文档就退出」的短命进程则是纯亏：花 2.4 秒省 1.9 毫秒。

它是一个**请求，不是保证**。编译器后端需要主流操作系统上的 amd64 或 arm64，还需要宿主允许 mmap 可执行页。条件不满足时——riscv64、ppc64le、386、macOS hardened runtime、某些 seccomp 策略——wazy 会**静默退回解释器**，转换照常完成，只是速度是解释器的速度。

**不影响交叉编译**：wazy 是纯 Go，这个选项不引入任何构建约束。

### `WithCompilationCache`

把编译器的产物持久化到你指定的目录，于是 `WithCompiler` 那笔成本**从每个进程付一次变成每台机器付一次**：

| 带 `WithCompiler` 的 `New()` | 耗时 | 峰值 RSS |
|---|---|---|
| 冷启动——正在编译 | 2.5 s | 630 MB |
| 热启动——把结果读回来 | **7 ms** | **36 MB** |

这个差距把反对 `WithCompiler` 的理由整个消掉了。`WithCompiler` 费内存的部分是**编译器在工作**，不是编好的模块躺在那里；命中缓存时是加载机器码，而不是生产机器码。缓存目录约 15 MB。

**只对 `WithCompiler` 有效。** 解释器不生成机器码，没有可持久化的产物，这个选项对它什么也不做——目录会一直是空的，测试里有断言盯着这一点。

缓存条目的键由模块内容、CPU 特性位、wazy 版本和目标平台共同决定。任何一项不同都是未命中，而未命中只是重新编译并写入新条目，**绝不会把宿主跑不了的机器码交给它**。所以这个目录是可丢弃的——删掉的代价就是重编译一次——而且里面装的是机器码，不含任何调用方传给 `Convert` 的内容。

由此有两条实践建议：给它一个能在重启后留存的位置，否则就白设了；以及让它**在运行时自然填充**，而不是烘进镜像——CPU 特性位在键里，而 CI 构建机与镜像最终落地的机器很少是同一种 CPU。

`WithMemoryLimitPages` 是在 `New` 时对照模块声明的最小内存校验的，**不是转换时**——设太低会立刻报错并说明原因，而不是过一会儿变成一个莫名其妙的转换失败。

单个 guest 实际需要多少内存，取决于**解压后**的内容体积，而不是文件在磁盘上的大小——zip 炸弹在磁盘上很小、在内存里很大，这个上限就是为此存在的：

| 文档 | 所需页数 |
|---|---|
| 模块最小值，什么都转不了 | 64（4 MiB） |
| docx，正文 0.4 MB | 192（12 MiB） |
| docx，正文 2 MB | 576（36 MiB） |
| PDF，文件 7.5 MB | 512–1024（32–64 MiB） |
| docx，正文 5 MB | 1280（80 MiB） |

默认 1024 页能转换一份普通的 7.5 MB PDF。如果某些文档在别处能转、在这里失败，就调高它——guest 内存耗尽时错误信息会明确说明，并点名这个选项。注意每个并发 guest 都可能占满这个额度，所以 `WithConcurrency` 乘以这个值才是你真正的内存上限。

## 开发

用 [Task](https://taskfile.dev) 驱动，`task --list` 列出全部任务。

```
task test      # 构建 wasip1 测试 stub，跑宿主层测试套件
task verify    # CI 跑的全部检查，也是打 tag 前的清单
task wasm      # 重新构建内嵌模块，需要 Rust 1.88 + wasm-opt
```

测试分成两套，用 build tag 隔开：

- `anydoc_test.go` 在 `-tags anydoc_nowasm` 下跑，对象是用 Go 自己的 `wasip1` target 编出来的 stub。stub 遵守同样的 ABI 但不做任何转换，所以整个宿主层——stdio 接线、退出码映射、并发、取消、内存隔离——**没有 Rust 工具链也能测**。详见 `testdata/README.md`。
- `anydoc_real_test.go` 打的是 `!anydoc_nowasm` tag，因此**有 `anydoc.wasm` 时才会被编译**，测的是这套东西真正要干的活：真实转换输出、格式识别，以及 crate 实际会吐出的错误码。

本 README 里的每一个数字都由 `bench_test.go` 产出：

```
go test -run '^$' -bench . -benchtime 3x
ANYDOC_BENCH_PDF=big.pdf go test -run '^$' -bench PDF -benchtime 3x
```

docx 样本在基准里现场生成，所以一次 checkout 就足以复现。PDF 不行：小到能提交进仓库的 PDF，都轻到不值得测，所以那一项从环境变量取路径，没有就跳过。改动 crate 版本或运行时之后请把两条都重跑一遍——这些数字的可信度，不会超过它们所依据的那次构建。

`anydoc.wasm` 是提交进仓库的构建产物——Go module 没有构建步骤，提交进去的是什么，下游 `go get` 拿到的就是什么。它由 `task wasm` 从锁定版本的 crate 构建，是手工执行而非 CI 自动完成的，`anydoc.wasm.sha256` 记录当前这份的校验和。复现它所需的工具链版本见 `anydoc.wasm.README`。

## 版本管理

**本模块的版本号和上游 crate 的版本号相互独立。** `rust/Cargo.toml` 里用 `=` 精确锁定 crate 版本，换到新的上游版本是一个有意识的动作：改 pin，跑 `task wasm`，评审转换输出发生了什么变化，然后打 tag。上游自己发新版，对这个仓库没有任何影响。

`anydoc.EmbeddedAnydocVersion` 报告内嵌模块是从哪个 crate 版本构建的，`anydoc.Info()` 把它和载荷大小一起打印。它由 `task sync-version` 从 `rust/Cargo.lock` 单向复制而来，`task check-version` 会在两者不一致时让构建失败——所以它不可能悄悄谎报实际内嵌的是什么。

`rust/Cargo.lock` 也一并提交。`=` 只锁住了 anydoc 本身，真正防止传递依赖在两次重建之间悄悄改变转换输出的，是这个 lockfile。

### 发布流程

```
task verify                # 全部检查
git tag -a vX.Y.Z && git push origin vX.Y.Z
```

**没有别的东西需要同步。** 运行时实验此前活在一个独立分支上，靠 `replace` 和一个预发布 tag 持有，每次发版都得小心别把它盖过去；wazy 发出正式 tag 之后这个分支就没有存在必要了，`main` 现在像依赖任何其他模块一样依赖它。

## 许可证

本包 MIT，见 `LICENSE`。内嵌模块从 [anydoc](https://github.com/firecrawl/anydoc) 构建，同样是 MIT——见 `LICENSE-anydoc`。
