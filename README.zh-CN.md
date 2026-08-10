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

需要 Go 1.25 或更高版本，这是 wazero 的要求。

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

**用解释器而不是优化编译器。** wazero 有两种执行引擎：一种在加载时把 wasm 翻译成宿主机原生机器码（跑得快，加载贵，只支持 amd64/arm64），另一种逐条解释执行（加载几乎免费，哪都能跑，跑得慢）。本包**默认**用解释器——一个要塞进别人二进制里的库，没法假定自己能在用户机器上预热编译缓存，没法假定宿主允许编译器需要的可执行内存页（macOS hardened runtime 和某些 seccomp 策略会直接拒绝），更没法假定目标平台是 amd64 或 arm64。需要另一头的取舍时用 `WithCompiler` 显式打开。

代价是真实存在的，而且随文档体积放大，上生产前请拿自己的语料实测：

| | 解释器（默认） | 编译器（`WithCompiler`） |
|---|---|---|
| `New()`——每进程一次 | 约 100 ms | 约 2.7 s |
| 1 KB docx | 3.5 ms | 0.4 ms |
| 解压后正文 5 MB 的 docx | 11.1 s | 0.86 s |
| 7.5 MB PDF | 41.4 s | 3.4 s |
| `New()` 之后的 RSS | 182 MB | 638 MB |

常规办公文档落在第二行，几毫秒，没有优化价值。上兆字节的文档则比编译模式慢约 **12 倍**——如果你要处理这类文档，要么用 `WithMaxInputBytes` 加 context 超时把长尾兜住（取消会当场打断 guest），要么显式打开 `WithCompiler`。

**慢的是运行时，不是 wasm。** 上面那些数字很容易被读成"编译到 WebAssembly 的代价"。拿同一份 PDF、**同一份 `anydoc.wasm`** 实测下来并不是——wasm 这个格式本身几乎不花钱，差距几乎全在于某个运行时如何把 wasm 变成机器码：

| 同一份 7.5 MB PDF | | 相对原生 |
|---|---|---|
| 原生 Rust 二进制 | 0.28 s | 1× |
| 同一份 wasm，wasmtime + Cranelift | 0.37 s | 1.3× |
| 同一份 wasm，wasmtime + Winch（刻意不优化的基线编译器） | 0.90 s | 3.2× |
| 同一份 wasm，wazy + `WithCompiler`（见下） | 0.75 s | 2.7× |
| 同一份 wasm，wazero + `WithCompiler` | 3.4 s | 12× |
| 同一份 wasm，wazy 解释执行（见下） | 33 s | 118× |
| 同一份 wasm，wazero 解释执行 | 41 s | 146× |

也就是说，通常被归咎的那些因素——32 位指针、带边界检查的线性内存、没有原生 SIMD——**加起来只值 1.3 倍**。超出这个数的部分全是代码生成质量。wazero 的编译器追求的是快、简单、可移植，而不是优化，而这恰恰正是"纯 Go、无 cgo、不需要为每个平台分发后端"所买到的东西。

把取舍摊开说：wasmtime 的 Go 绑定是 cgo 的，采用它就要付出 `CGO_ENABLED=0`、自由交叉编译和单一自包含二进制这三样。如果你的场景里原生速度比这三样更重要，那么一个 cgo 绑定才是诚实的答案，本包不是合适的工具。

### `experiment-wazy` 分支

上面那个差距，大部分并不是"坚持纯 Go"的必然代价。[wazy](https://github.com/samyfodil/wazy) 是一个由 wazero 衍生而来的纯 Go 运行时，它把力气花在了主导这类负载的内存访问路径上。[`experiment-wazy`](../../tree/experiment-wazy) 分支就是本包跑在它上面——API、内嵌模块、退出码 ABI 全都没变，只改了一处 import：

| 转换 | `main`（wazero） | `experiment-wazy` | |
|---|---|---|---|
| 1 KB docx，解释器 | 3.5 ms | 2.7 ms | 1.3× |
| 1 KB docx，编译器 | 0.4 ms | 0.62 ms | 0.6× |
| 5 MB docx 正文，解释器 | 11.1 s | 8.6 s | 1.3× |
| 5 MB docx 正文，编译器 | 0.86 s | 0.19 s | 4.5× |
| 7.5 MB PDF，解释器 | 41.4 s | 32.6 s | 1.3× |
| 7.5 MB PDF，编译器 | 3.4 s | 0.75 s | 4.5× |

输出逐字节一致，完整测试套件通过，交叉编译到 riscv64、ppc64le、386、s390x 同样正常。优势集中在长时间计算；小文档基本持平甚至略差。

用法是直接按分支名取，Go 会把它解析成一个伪版本：

```bash
go get github.com/xusenlin/go-anydoc@v0.1.3-experiment.1
```

其余什么都不用改：导入路径一样，API 一样。这个 tag 是 semver 预发布版本，Go 在
`@latest` 时会跳过它——不会有人误装。

有一点需要知道：预发布版本只低于**同号的正式版**，但高于所有更低的版本。所以一旦
`main` 发布了 `v0.1.3` 之后的 tag，实验版用户跑 `go get -u` 就会被切回 `main`，
运行时也就悄悄换了回去。除此之外没有任何东西会动你的 `go.mod`——`go build` 和
`go mod tidy` 都不会——所以请把版本钉死，并避免对这个依赖使用 `-u`。每次 `main`
发版时实验 tag 都会重新打到更高的号上以保持 `-u` 的语义正确，但不要只依赖这一点。

**做成分支而不是选项，是因为其他做法的代价。** 在调用时选择运行时，会把两个引擎都链接进每一个二进制——实测让只用其中一个的用户多付 **3.22 MB**。build tag 能避免链接开销，但无论哪种做法，wazy 都会进入每个下游用户的模块图。而 wazy 目前没有稳定版本：几个被撤回的 beta、一个伪版本、单一作者，并且明确声明不作稳定性承诺。`main` 继续用 wazero，就是为了不让任何人在不知情的情况下继承这份风险。愿意承担风险换速度的，可以直接用这个分支；等 wazy 发布稳定版后会重新评估。

<sub>实测环境：Apple M5 Pro，macOS 26.5，Go 1.26.1，wazero v1.12.0，`anydoc.wasm` 6,542,355 字节（anydoc 0.1.7）。5 MB 那一行是 2.5 万行表格，zip 后只有 42 KB——真正决定耗时的是解压后的正文体积，不是文件大小。</sub>

**每份文档一个全新 guest。** 每次 `Convert` 都新建独立的线性内存，所以一份大文档不会让内存被永久占住，一份畸形文档也不会把状态泄漏给下一次调用。真正昂贵的编译只在 `New` 里做一次。

**用 command 模块而不是导出函数。** guest 读 stdin 写 stdout，Go 侧完全不用碰线性内存、指针和 UTF-8 边界，错误类型由退出码携带。见 `rust/src/main.rs`——那里的退出码表和 `errors.go` 是一份跨两种语言的契约。

**沙箱。** 只挂 WASI 的 stdio 和 `random_get`（`HashMap` 播种要用）。不给文件系统，不给时钟，不给套接字。

## 控制体积

默认内嵌模块，`go get` 完 `New()` 就能跑。想把载荷单独分发的：

```
go build -tags anydoc_nowasm    # 省下 6.59 MB
```

此时 `embeddedWASM` 为 nil，`New` 要求必须传 `WithWASM(r)` 或 `WithWASMBytes(b)`。适用于容器分层、Serverless 部署包大小限制、锁定另一个 anydoc 构建，或者禁止二进制内嵌不可追溯 blob 的合规环境。

省下的是 6.54 MB 的模块本身，再加上约 47 KB 的 `embed` 机制开销。给个体感：`examples/convert` 正常编译 14.1 MB，加上这个 tag 是 7.6 MB。

注意 build tag 只影响编译产物，不影响 `go get`——模块文件在 Go module 里躺着，两种情况都要下载。

## 参数调节

```go
anydoc.New(
    anydoc.WithConcurrency(4),        // 同时运行的 guest 数，控制峰值内存的主要手段
    anydoc.WithMemoryLimitPages(1024), // 单个 guest 64 MiB
    anydoc.WithMaxInputBytes(64<<20),
    anydoc.WithCompiler(),            // 用启动成本换吞吐，见下
)
```

### `WithCompiler`

把模块编译成原生机器码而不是解释执行，效果就是把上面那张表从左列变成右列。

这笔成本**只在 `New` 里付一次**，不是每份文档都付——`Convert` 只做实例化，用的是已经编好的模块。所以它适合长期运行、复用同一个 `Converter` 的进程；对于「转一份小文档就退出」的短命进程则是纯亏：花 2.1 秒省 4 毫秒。

它是一个**请求，不是保证**。编译器后端需要主流操作系统上的 amd64 或 arm64，还需要宿主允许 mmap 可执行页。条件不满足时——riscv64、ppc64le、386、macOS hardened runtime、某些 seccomp 策略——wazero 会**静默退回解释器**，转换照常完成，只是速度是解释器的速度。

**不影响交叉编译**：wazero 是纯 Go，这个选项不引入任何构建约束。

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

`anydoc.wasm` 是提交进仓库的构建产物——Go module 没有构建步骤，提交进去的是什么，下游 `go get` 拿到的就是什么。它由 `task wasm` 从锁定版本的 crate 构建，是手工执行而非 CI 自动完成的，`anydoc.wasm.sha256` 记录当前这份的校验和。复现它所需的工具链版本见 `anydoc.wasm.README`。

## 版本管理

**本模块的版本号和上游 crate 的版本号相互独立。** `rust/Cargo.toml` 里用 `=` 精确锁定 crate 版本，换到新的上游版本是一个有意识的动作：改 pin，跑 `task wasm`，评审转换输出发生了什么变化，然后打 tag。上游自己发新版，对这个仓库没有任何影响。

`anydoc.EmbeddedAnydocVersion` 报告内嵌模块是从哪个 crate 版本构建的，`anydoc.Info()` 把它和载荷大小一起打印。它由 `task sync-version` 从 `rust/Cargo.lock` 单向复制而来，`task check-version` 会在两者不一致时让构建失败——所以它不可能悄悄谎报实际内嵌的是什么。

`rust/Cargo.lock` 也一并提交。`=` 只锁住了 anydoc 本身，真正防止传递依赖在两次重建之间悄悄改变转换输出的，是这个 lockfile。

### 发布流程

```
task verify                # 全部检查
git tag -a vX.Y.Z && git push origin vX.Y.Z
task check-experiment-tag  # 然后把实验 tag 重打到新版本之上
```

**只要 `experiment-wazy` 还在，最后一步就不是可选的。** semver 预发布版本只输给同号正式版、赢过所有更低版本，所以 `main` 上的新 tag 会爬到 `vX.Y.Z-experiment.N` 之上，`go get -u` 就会把实验版用户拉回 `main`——运行时被悄悄换掉，且没有任何提示。`task check-experiment-tag` 会在这种情况下报错，届时把实验分支重打成比新版本高一个 patch 的 tag 并推送即可。

## 许可证

本包 MIT，见 `LICENSE`。内嵌模块从 [anydoc](https://github.com/firecrawl/anydoc) 构建，同样是 MIT——见 `LICENSE-anydoc`。
