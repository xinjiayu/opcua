# PATCHES

本仓库是 `github.com/gopcua/opcua` 的 fork，带有安全补丁。

- **`sagooiot-v0.9.3`**：当前使用的分支，从上游标签 `v0.9.3` 拉出，发布标签 `v0.9.3-sagooiot.3`（前一版 `v0.9.3-sagooiot.2`）。本文件描述的就是它。
- **`sagooiot-v0.8.0`**：较早的分支，从上游标签 `v0.8.0` 拉出，发布标签 `v0.8.0-sagooiot.1`，保留作回退。
  带全部 14 条补丁（含上游后来才修的 P1、P3、P7a），两处行为与上游不同，行号也与本文件不同。
- **`main`**：原样镜像上游，不带补丁。

这份文件是 `sagooiot-v0.9.3` 相对上游 v0.9.3 **全部**改动的清单，也是将来与上游比对、合并上游修复时的唯一依据。

## 0. 先读这几条

- **`go.mod` / `go.sum` 与上游 v0.9.3 逐字节一致，没有升版本、没有抬 `go` 行。**
  依赖声明保持原样，是为了能直接和上游 diff。使用方（SagooIoT 主模块）通过 `replace` 使用本仓库，
  依赖版本由使用方决定。
- **在本仓库里单独构建时，上游钉的部分依赖版本可能需要联网获取**
  （例如 `golang.org/x/exp@v0.0.0-20241204233417-43b7b7cde48d`、`golang.org/x/term@v0.27.0`、`testify@v1.10.0`）。
  使用方选中的是更高的版本，所以这些版本不一定在模块缓存里。缓存里没有的时候，
  离线环境下 `go build` 或 `go test` 会在拉取依赖时失败。这与补丁无关。
- 本分支带 13 条补丁（P2、P4–P15），源码改动集中在下表列出的文件里。
  P1、P3、P7a 上游在 v0.9.3 里已经修了，本分支不含这三条改动，只保留它们的测试作回归用例。
  新增的测试全部以 `patch_` 开头，上游没有同名文件，合并上游时不会冲突。
- `go vet ./server/ ./ua/` 在**未改动的上游代码**上就不过：`ua/buffer.go:71`、`:249` 的
  `ReadByte`/`WriteByte` 签名触发 `stdmethods` 检查（对模块缓存里的上游原件跑得到同样两条）。
  请用 `go vet -stdmethods=false`。
- `uacp` 包的 `TestResolveEndpoint` 是上游既有的测试，它解析一个公网主机名，
  依赖真实 DNS，本机因代理的 fake-ip 解析而失败。与补丁无关。

## 1. 怎么跑测试

在本仓库里：

```sh
GOWORK=off go build ./...
GOWORK=off go vet -stdmethods=false ./server/ ./ua/
GOWORK=off go test -race -count=1 -skip 'TestResolveEndpoint$' ./server/ ./ua/ ./uasc/ ./uacp/
```

`-skip` 跳过的是上面提到的 `TestResolveEndpoint`（依赖真实 DNS）。

在使用方里（`replace` 加好之后）：

```sh
cd sagooiot-professional
go test -race github.com/gopcua/opcua/server github.com/gopcua/opcua/ua github.com/gopcua/opcua/uasc github.com/gopcua/opcua/uacp
go vet -stdmethods=false github.com/gopcua/opcua/server github.com/gopcua/opcua/ua github.com/gopcua/opcua/uacp
```

`tests/` 目录是需要外部服务器的集成测试，不在上述范围内。

会崩溃的缺陷无法用普通测试断言，所以相应的用例把被测代码放进子进程里跑
（`server/patch_test.go` 的 `crashProbe`，写法同 Go 标准库的 `TestCrasher`），
把「进程死了」变成一条普通的断言失败。

## 2. 补丁一览

| 编号 | 问题 | 类型 | 改动位置 | 上游 v0.9.3 |
|---|---|---|---|---|
| P1 | 向未注册的命名空间 Write 使进程退出 | 崩溃 | `server/attribute_service.go` | **已修**；本分支无改动，仅保留测试 |
| P2 | 浏览 ns0 节点使进程退出 | 崩溃 | `server/view_service.go` | 未修 |
| P3 | 解码器按报文声明的数组长度预分配内存 | 资源耗尽 | `ua/decode.go` | **已修**；本分支无改动，仅保留测试 |
| P4 | 订阅服务的四个问题 | 崩溃 / 资源 | `server/subscription_service.go` | 未修 |
| P5 | 监视项服务的两个问题 | 崩溃 / 越权 | `server/monitored_item_service.go` | 未修 |
| P6 | 集中的会话校验 | 越权 | `server/service_handlers.go`、`server/session_broker.go` | 未修 |
| P7a | 会话超时的单位错误 | 缺陷修正 | `server/session_service.go` | **已修**；本分支无改动，仅保留测试 |
| P7 | 会话数量上限与过期清理 | 资源耗尽 | `server/session_broker.go`、`server/server.go` 等 | 未修 |
| P8 | `GetEndpoints` 与 `CreateSession` 的地址匹配 | 互操作 | `server/discovery_service.go`、`server/session_service.go` | 未修 |
| P9 | 订阅号撞号 | 缺陷 | `server/subscription_service.go` | 未修 |
| P10 | `ReverseHello` 让服务端主动外连 | SSRF | `uacp/conn.go` | 未修 |
| P11 | `CloseSession` 不处理 `DeleteSubscriptions` | 资源泄漏 | `server/session_service.go` | 未修 |
| P12 | 解码递归没有深度上限 | 崩溃 / 资源耗尽 | `ua/decode.go`、`ua/buffer.go`、`ua/variant.go` 等 6 个 | 未修 |
| P13 | `FindServers` 不检查端点列表 | 崩溃 | `server/discovery_service.go` | 未修 |
| P14 | `ActivateSession` 不校验用户名口令 | 越权 | `server/session_service.go`、`server/server_config.go`、`server/server.go`、`uasc/secure_channel_crypto.go` | 未修 |
| P15 | 未激活会话可 Read/Write（CreateSession 后跳过 ActivateSession） | 越权 | `server/session_broker.go`、`server/session_service.go`、`server/service_handlers.go` | 未修 |

「上游 v0.9.3」一列由主控在 2026-11-26 对照 v0.9.3 的源码逐条核对：只有 P1、P3、P7a 上游已修，
其余（含 P14）都还在，上游主干（未发版的 `main`）同样没修。本分支基于 v0.9.3，所以只带这些补丁。

换基线时记下的事实：v0.9.3 相对 v0.8.0 改了客户端一侧的 6 个文件（`client.go`、`client_sub.go`、
`config.go`、`node.go`、`subscription.go`、`monitor/subscription.go`）以及 `uasc`、`uapolicy`、`ua`、
`uacp` 的核心文件，两者的 `go.mod` 依赖完全相同，仓库里的 CHANGELOG 停在 v0.8.0。
SagooIoT 的南向采集插件 `plugins/opcua` 是独立模块，生产构建用 `GOWORK=off` 和它自己的 `go.mod`，
不经过这里的 `replace`，所以换基线不影响它的生产构建。

对使用方可见的一处变化：v0.9.3 的 `Browse` 处理器按 `RequestedMaxReferencesPerNode` 截断结果并附一个
`ContinuationPoint`，剩下的引用靠 `BrowseNext` 续取；v0.8.0 忽略这个页大小。自己应答 `BrowseNext` 的使用方
要对齐两者，否则设了页大小的客户端只能拿到第一页，其余的取不回来。

2026-11-26 从 v0.8.0 换到 v0.9.3 的经过：补丁整体套用，冲突只有上游已经修过的那几处（P1、P3、P7a），
按上游为准；P1 和 P7a 的测试预期改成上游的边界；其余 11 条补丁的改动行逐字未变。
上游 v0.9.3 另外修了几处本仓库只记在审计清单里的问题，见第 5 节开头。

行号均指 `sagooiot-v0.9.3` 分支的当前状态。各条下面的「未打补丁时的失败表现」是换基线当天在这个基线上
逐条撤掉补丁后重新采集的：**11 条补丁每撤掉一条，针对它的测试都会变红**，没有一条是多余的；
其中的行号是未打补丁的上游 v0.9.3 里出错的位置。P1、P3、P7a 上游已修，它们的「未打补丁时的失败表现」
是在 v0.9.3 上把上游的修复撤回去之后采集的，同样不取自 v0.8.0。

## 3. 逐条补丁

### P1　向未注册的命名空间 Write 使进程退出

> **上游 v0.9.3 已修，本分支不含这条补丁。** 下面描述的是 `sagooiot-v0.8.0` 分支上的补丁；
> 本分支只保留测试 `TestWriteUnregisteredNamespace` 作回归用例。上游返回的状态码是
> `BadNodeIDUnknown`，`sagooiot-v0.8.0` 分支的补丁用的是 `BadNotInView`；本分支的测试按上游的状态码断言。

- **改动**：`Write` 里查不到命名空间时补上 `continue`。上游 v0.9.3 的修法相同，
  在 `server/attribute_service.go:85-88`，状态码是 `BadNodeIDUnknown`。
- **问题**：设置了 `BadNodeNotInView` 之后没有跳过，接着对 nil 的命名空间调用 `SetAttribute`。
  同一文件里的 `Read`（第 41 行）本来就有 `continue`。
- **触发条件**：一条 `WriteRequest`，`NodeId=ns=99;s=x`。不需要会话。
- **上游参考**：v0.9.3 已修，做法就是补 `continue`。
- **测试**：`server/patch_p1_write_test.go` — `TestWriteUnregisteredNamespace`
  （三个用例：只有未注册命名空间、未注册在前、未注册在后；后两个确认结果与请求仍然一一对齐）。
- **未打补丁时的失败表现**（在 v0.9.3 上把 `Write` 换回上游 v0.8.0 的写法后采集）：
  ```
  patch_p1_write_test.go:40: unregistered-only: probe child did not exit cleanly ... exit status 2
  panic: runtime error: invalid memory address or nil pointer dereference
      server/attribute_service.go:89
  ```

### P2　浏览 ns0 节点使进程退出

- **改动**：`server/view_service.go:164-170`，`suitableRefType` 里的循环换成
  `oktypes = slices.DeleteFunc(oktypes, hasSubtypeFn)`。
- **问题**：原循环 `for n := slices.IndexFunc(...); n > 0; { oktypes = slices.Delete(oktypes, n, n+1) }`
  的下标 `n` 从不更新，而切片每删一次就短一格。只要 `HasSubtype` 不是列表的第一项，
  循环就会先删错元素，最后越界。
- **意图**：不带子类型浏览时，把 `HasSubtype` 本身从允许的引用类型里去掉。替换后就是这个结果，
  与 `HasSubtype` 在列表里的位置无关。
- **触发条件**：浏览一个 ns0 节点（如 ObjectsFolder，i=85），`ReferenceTypeId` 取
  `HasSubtype` 的某个父类型，且 `IncludeSubtypes=false`。
  出厂的 NodeSet2 里这样的父类型有**三个**，不止任务书列的两个：

  | ReferenceTypeId | HasSubtype 在子类型表里的下标 |
  |---|---|
  | References (31) | 54 |
  | HierarchicalReferences (33) | 20 |
  | HasChild (34) | 19 |

  不需要会话。
- **上游参考**：v0.9.3 未修这个循环。
- **行为变化**：出厂 ns0 里没有任何引用类型把 `HasSubtype` 排在下标 0，所以原代码「下标为 0 时
  循环不执行、漏删」的分支用标准地址空间走不到。只有自定义命名空间定义了这种引用类型时才会遇到，
  那种情况下原代码会错误地放行 `HasSubtype` 引用，现在会排除它。
- **测试**：`server/patch_p2_browse_test.go`
  - `TestSuitableRefTypeNoCrash` — 三个父类型的八个变体，原先都会崩
  - `TestBrowseObjectsFolderNoCrash` — 经过 `Browse` 处理器，三个父类型
  - `TestSuitableRefTypeExcludesHasSubtypeListedFirst` — 合成命名空间，`HasSubtype` 排在下标 0
  - `TestSuitableRefTypeUnchanged`、`TestBrowseObjectsFolderUnchanged` — 原先不崩的输入，
    期望值取自在未打补丁的代码上记录的结果（Organizes、含子类型的 References、不含子类型的
    NonHierarchicalReferences、显式 TwoByte(0)）
- **未打补丁时的失败表现**（逐个父类型各跑一次）：
  ```
  References:              panic: runtime error: slice bounds out of range [:55:54]   view_service.go:166
  HierarchicalReferences:  panic: runtime error: slice bounds out of range [:21:20]   view_service.go:166
  HasChild:                panic: runtime error: slice bounds out of range [:20:19]   view_service.go:166
  下标 0 的合成用例:        HasSubtype reference accepted although subtypes are excluded
  ```

### P3　解码器按报文声明的数组长度预分配内存

> **上游 v0.9.3 已修，本分支不含这条补丁。** 下面描述的是 `sagooiot-v0.8.0` 分支上的补丁，它本来就是从
> 上游移植的；在 v0.9.3 基线上这段代码是上游自己的。本分支只保留测试作回归用例。

- **改动**：`ua/decode.go:192-202`，`decodeSlice` 在 `reflect.MakeSlice` 之前检查
  `int(n) > buf.Len()`，不满足就返回
  `array length %d exceeds remaining %d bytes`。
- **问题**：数组长度直接来自报文，原先只拦 `n > MaxInt32`。一条 45 字节的 `ReadRequest`
  把 `NodesToRead` 的长度改成 5000 万，就会在读任何元素之前分配 400 MB。
  多个连接并发时内存成倍增长。
- **为什么安全**：UA 二进制编码里每个数组元素在线上至少占 1 个字节，所以「长度大于剩余字节数」
  的数组永远不可能被满足，这个检查不会拒绝任何合法报文。
- **触发条件**：任何带数组的请求，不需要会话。解码器客户端与服务端共用，两个方向都受影响。
- **上游参考**：v0.9.3 已修，`sagooiot-v0.8.0` 分支的补丁从它移植，代码与注释逐字照搬。
- **`decodeArray`**：`ua/decode.go:226-278` 没有这个问题，未改动。见第 4 节。
- **对客户端的影响**：合法报文的解码行为不变。变化的只有畸形报文的错误文本
  （`unexpected EOF` 变成 `array length ... exceeds remaining ... bytes`）；
  库、网关、南向插件里没有任何代码依赖旧的错误。
- **测试**：
  - `ua/patch_p3_decode_test.go`
    - `TestDecodeRejectsArrayLongerThanInput` — 45 字节、声明 5000 万，断言返回错误且分配量小于 1 MiB
    - `TestDecodeArrayLengthUpToInputSize` — 每个元素恰好 1 字节的边界：正好装下、少一个、多一个、
      远超输入、超过 `MaxInt32`、空数组、null 数组
    - `TestDecodeAcceptsOrdinaryRequest` — 普通的 `ReadRequest` 编码再解码，结果不变
    - `TestDecodeArrayLengthIsBoundedByArrayType` — 钉住 `decodeArray` 的现状
  - `server/patch_p3_client_test.go` — `TestClientDecodesLargeRealResponses`：
    用本库的客户端解真实的服务端响应（500 个结果的 `ReadResponse`、带引用的 `BrowseResponse`）
- **未打补丁时的失败表现**（在 v0.9.3 上撤掉这段检查后采集）：
  ```
  decoding 45 bytes that declare 50000000 elements allocated 400061504 bytes
  Error "unexpected EOF" does not contain "array length 50000000 exceeds remaining 0 bytes"
  "400061504" is not less than "1048576"
  ```
  上游的检查生效时同一输入：`allocated 912 bytes`。

### P4　订阅服务

改动都在 `server/subscription_service.go`。上游 v0.9.3 未修（2026-11-26 核对）。

**(a) 未知会话也能建订阅** — 第 104-118 行

- **问题**：`s.srv.Session()` 对不认识的令牌返回 nil，处理器照样建订阅并启动 `run()`。
  几个发布间隔之后，保活路径在这个没有 recover 的协程里解引用会话，进程退出。
- **改动**：会话查找放在 `SubscriptionService.Mu` 之下，查不到返回 `ua.StatusBadSessionIDInvalid`。
  查找放在锁里是 P7 的要求，见 P7。
- **测试**：`TestCreateSubscriptionRejectsUnknownSession`
- **未打补丁时**：
  ```
  panic: runtime error: invalid memory address or nil pointer dereference
      server/subscription_service.go:389   （协程从 :303 的 go s.run() 启动）
  ```

**(b) 发布间隔原样采用客户端的值** — 第 15-35、131-134、153 行

- **问题**：`RequestedPublishingInterval` 不做处理就交给 `time.NewTicker`。0、负数、NaN、
  正负无穷、大到溢出的值都会 panic，而 `run()` 没有 recover。
- **改动**：钳制到 `[100ms, 1h]`，上下限是包级常量 `minPublishingIntervalMS`、`maxPublishingIntervalMS`。
  NaN 与无穷按「不大于等于下限」处理，取下限。修订值同时写进
  `sub.RevisedPublishingInterval` 和响应的 `RevisedPublishingInterval`。
- **测试**：`TestCreateSubscriptionClampsPublishingInterval`（十二个用例）
- **未打补丁时**：

  | 请求值 | 表现 |
  |---|---|
  | 0、-5、NaN、+Inf、-Inf | `panic: non-positive interval for NewTicker`（`subscription_service.go:354`） |
  | 99.999 | 不崩，原样返回 99.999 ms |
  | 3600001、1e18 | 不崩，原样返回 |
  | 100、1000、250.5、3600000 | 正常（钉住） |

**(c) 未知会话删订阅** — 第 277-295 行

- **问题**：拿到 nil 会话后直接 `session.AuthTokenID.String()`。请求里至少要有一个存在的订阅号才会走到这一步。
- **改动**：会话为 nil 时，每个订阅号的结果都是 `ua.StatusBadSessionIDInvalid`，不删除任何东西。
- **测试**：`TestDeleteSubscriptionsRejectsUnknownSession`；
  `TestDeleteSubscriptionsOwnership` 钉住原本就正确的「只能删自己的订阅」
- **未打补丁时**：`nil pointer dereference`，`server/subscription_service.go:228`

**(d) 订阅协程没有 recover** — 第 375-393 行

- **改动**：`Start()` 启动的协程带 recover。恢复时打一条带完整堆栈的 Error 日志，没有配置日志器时
  用标准库 `log`。`run()` 自己的 defer 在此之前已经把订阅删掉了。
- **测试**：`TestSubscriptionRunRecoversFromPanic`（有日志器、无日志器两个用例）
- **未打补丁时**：`nil pointer dereference`，最里层在 `uasc/secure_channel.go:327`（`getActiveChannelInstance`），
  由 `run()` 里的 `server/subscription_service.go:332` 调进去，协程从 `:303` 启动。

以上测试都在 `server/patch_p4_subscription_test.go`。

### P5　监视项服务

改动在 `server/monitored_item_service.go`。上游 v0.9.3 未修（2026-11-26 核对）。

- **改动**：新增 `itemStatus`（第 324-344 行，调用方必须持有 `Mu`），
  `SetMonitoringMode` 与 `DeleteMonitoredItems` 的循环都改成先调用它
  （`status := s.itemStatus(id, sess)` 在第 288 行和第 365 行），再按状态码决定是否继续。
- **问题 (a)**：两个处理器都先解引用 `item` 再判断 `ok`。请求里带一个不存在的监视项号就 panic，
  而且发生在唯一的请求处理协程里。
- **问题 (b)**：对「存在但属于别的会话」的监视项，先写了 `BadSessionIDInvalid`，
  随后被 `StatusOK` 覆盖。任何客户端都能删掉或改掉别的客户端的监视项，并得到成功的响应。
- **触发条件**：需要有效会话。(a) 只要带一个不存在的监视项号；(b) 带别的会话的监视项号。
- **测试**：`server/patch_p5_monitoreditem_test.go`
  - `TestDeleteMonitoredItemsRejectsUnknownID`、`TestSetMonitoringModeRejectsUnknownID`
    （未知号在前、在后、单独，共六个变体）
  - `TestDeleteMonitoredItemsRejectsOtherSession`、`TestSetMonitoringModeRejectsOtherSession`
  - `TestMonitoredItemHandlersServeOwner` — 属主自己的请求正常，钉住
- **未打补丁时的失败表现**：
  ```
  DeleteMonitoredItems:   nil pointer dereference   monitored_item_service.go:351
  SetMonitoringMode:      nil pointer dereference   monitored_item_service.go:287

  只关掉 itemStatus 里的令牌比较时，对别的会话的监视项（11 号）：
  DeleteMonitoredItems:  results for [11 999999] = [StatusGood, BadMonitoredItemIDInvalid]
  SetMonitoringMode:     同上
  ```
  前两行是撤掉整条补丁后两个崩溃用例的直接输出；后两行是在 v0.9.3 上只关掉 `itemStatus` 里的
  令牌比较时采的：别的会话的监视项操作成功，不存在的监视项号（999999）仍然报无效。

### P6　集中的会话校验

- **改动**：
  - `server/service_handlers.go:18-37`：允许表 `sessionlessServices`
  - `server/service_handlers.go:124-176`：`handleService`，分派逻辑重写（第 132-156 行）
  - `server/service_handlers.go:178-190`：`sessionFor`
  - `server/session_broker.go:22-25`（`lastActivity`）、`:111-120`（`lookup`）、`:122-128`（`touch`）
- **问题**：`handleService` 把请求原样交给处理器，除了建立会话要用的几个服务，其余服务都不要求会话。
  不建会话的 Read、Write、Browse 照样返回真实数据。
- **改动内容**：允许表以外的服务，若请求带的令牌查不到会话，直接回
  `ServiceFault(BadSessionIdInvalid)`，不进入处理器。允许表里是
  FindServers、FindServersOnNetwork、GetEndpoints、RegisterServer、RegisterServer2、
  CreateSession、ActivateSession。CloseSession 需要一个存在的会话，不在表内。
- **为什么是白名单**：黑名单会让以后新增的服务（库里加的，或者应用通过 `RegisterHandler` 加的）
  在有人记得把它列进去之前都不要会话就能用。白名单让新服务默认受保护。
- **活动时间**：每个带有效会话的请求都调用 `touch` 刷新该会话的 `lastActivity`，供 P7 使用。
  集中校验用不出日志的 `lookup`，而不是会在查不到时打警告的 `Session`——
  没带会话的请求在会话建立之前是常态，不该让每个 `GetEndpoints` 多打一行警告。
- **触发条件**：建连之后、建会话之前发送任何服务请求。
- **对客户端可见的变化**：这类请求现在得到 `ServiceFault(BadSessionIdInvalid)`。
  其中 Publish 原先得到的是一个头里带错误码的 `PublishResponse`。
- **上游参考**：v0.9.3 未修（2026-11-26 核对）。
- **测试**：`server/patch_p6_session_test.go`（端到端，真实客户端）
  - `TestRequestsWithoutSessionAreRejected` — 13 个服务 × 空令牌 / 未知令牌，共 26 个子用例，
    并确认被拒绝的请求没有留下任何订阅、监视项、会话
  - `TestSessionlessServicesStillWork` — 发现服务在裸通道上照常，客户端完整建会话后 Read 拿到数据
  - `TestSessionlessServicesAllowlist` — 钉住允许表的内容
  - `TestRequestRefreshesSessionActivity` — 带会话的请求刷新活动时间，不带的不会
- **未打补丁时的失败表现**（26 个子用例里 24 个失败；另 2 个是 CreateSubscription，
  P4(a) 已经生效）：
  ```
  *ua.ReadRequest   was answered with *ua.ReadResponse carrying the value 2026-11-26 01:56:00.13871 +0000 UTC
  *ua.WriteRequest  was answered with *ua.WriteResponse (service result StatusGood)
  *ua.BrowseRequest was answered with *ua.BrowseResponse (service result StatusGood)
  *ua.CloseSessionRequest was answered with *ua.CloseSessionResponse (service result StatusGood)
  ...
  ```

### P7a　会话超时的单位错误

> **上游 v0.9.3 已修，本分支不含这条补丁。** 下面描述的是 `sagooiot-v0.8.0` 分支上的补丁；本分支只保留
> 测试 `TestCreateSessionRevisedTimeout` 作回归用例。上游的修法相同（常量改成 `time.Duration`），但上限取
> 1 小时，`sagooiot-v0.8.0` 分支的补丁取 30 分钟；本分支的测试按上游的边界断言。

这是一条独立的缺陷，不是 P7 的附属修正。P7 的过期清理要比较「上次请求时间」和会话的超时值，
所以依赖它。

- **改动**：三个常量从「毫秒数值」改成 `time.Duration`（上游 v0.9.3 的版本在 `server/session_service.go:13-15`）：
  ```go
  sessionTimeoutMin     = 100 * time.Millisecond   // 原 100
  sessionTimeoutMax     = 30 * time.Minute         // 原 30 * 60 * 1000
  sessionTimeoutDefault = 60 * time.Second         // 原 60 * 1000
  ```
  `server/session_service.go:39-42` 的比较逻辑和「超出范围取默认值」的规则都没变。
- **问题**：原常量注释写的是毫秒，却是无类型的整数；`sess.cfg.sessionTimeout` 是 `time.Duration`，
  单位是纳秒。于是「上限 1800000」实际是 1.8 毫秒，「下限 100」实际是 100 纳秒，
  任何大于 1.8 毫秒的请求都被判为越界，换成「默认值」60000 纳秒，也就是 60 微秒。
- **探针证据**（在 v0.9.3 上把三个常量撤回毫秒数值后，`TestCreateSessionRevisedTimeout` 的九个用例全部失败）：
  ```
  the client was told the session timeout is 0s, want 2m0s
  the server stored the session timeouts [60µs], want exactly one of 2m0s
  ```
  请求 120 秒、500 毫秒、100 毫秒、1 小时、2 小时、0、负数、50 毫秒，服务端存下的**全是 60µs**，
  回给客户端的**全是 0ms**。只有请求恰好是 1 毫秒时原样保留。

  | 客户端请求 | 撤掉修复：服务端存储 | 撤掉修复：`RevisedSessionTimeout` | 上游 v0.9.3 的修复：存储与响应 |
  |---|---|---|---|
  | 120s | 60µs | 0ms | 120s |
  | 500ms | 60µs | 0ms | 500ms |
  | 100ms | 60µs | 0ms | 100ms |
  | 1h（上限） | 60µs | 0ms | 1h |
  | 2h、0、负数、50ms | 60µs | 0ms | 60s（默认值） |
  | 1ms | 1ms | 1ms | 60s（默认值） |

- **对客户端可见的影响**：`CreateSessionResponse.RevisedSessionTimeout` 从 0ms 变成服务端实际授予的值。
  这是把一个错误的回答改对，不是新增行为。本库的客户端只把它存起来
  （`Session.RevisedTimeout()`，库内没有调用方）；其他客户端通常用它安排会话的保活。
  修前服务端没有任何地方读这个值，所以服务端行为不因这条修正而变。
  **如果不修，P7 的清理协程会把每个会话在上一次请求后 60 微秒就踢掉。**
- **上游参考**：v0.9.3 已修（2026-11-26 核对）：`sessionTimeoutMin = 100 * time.Millisecond`、
  `sessionTimeoutMax = 1 * time.Hour`、`sessionTimeoutDefault = 60 * time.Second`。
- **测试**：`server/patch_p7a_session_timeout_test.go` — `TestCreateSessionRevisedTimeout`
  （九个用例，同时断言客户端看到的修订值和服务端存储的值）。

### P7　会话数量上限与过期清理

- **改动**：
  - `server/session_broker.go:62`：`maxSessions = 64`
  - `server/session_broker.go:64-66`：`sessionSweepInterval`（变量，默认 10 秒，测试会调短）
  - `server/session_broker.go:68-95`：`NewSession(timeout time.Duration)` — 检查上限，
    带着超时值创建会话，并把 `lastActivity` 初始化为当前时间
  - `server/session_broker.go`：`expireIdle` — 在同一把锁内判断并移除超时的会话；
    名下有订阅且排着 Publish 请求的会话豁免（见下面的「等待应答的会话」）
  - `server/session_broker.go`、`server/subscription_service.go`：会话上的 `subscriptions` 计数，
    由 `CreateSubscription` / `DeleteSubscription` 经 `subscriptionAdded` / `subscriptionRemoved` 维护
  - `server/session_broker.go:199-206`、`:208-221`：`sweepSessions`、`reapSessions` — 每 10 秒一轮，
    随 `Start(ctx)` 的 ctx 结束
  - `server/session_broker.go:223-247`：`deleteSubscriptionsOf` — 删除一个会话名下的全部订阅
  - `server/server.go:271`：`Start` 里启动 `go s.reapSessions(ctx)`
  - `server/session_service.go:39-62`：先算出超时值；把 `NewSession` 挪到所有可能失败的步骤之后；
    拿不到会话时回 `BadTooManySessions`
  - `server/subscription_service.go:107-118`：`CreateSubscription` 的会话查找挪到持锁之后
  - `server/monitored_item_service.go:187-193`：`CreateMonitoredItems` 容忍 nil 会话
- **问题**：`NewSession()` 没有上限；`sessionTimeout` 算出来存下了，但没有任何地方使用，会话永不过期。
  任何客户端不用认证就能开任意多的会话，每个会话和它名下的东西都活到进程结束。
- **上限**：达到 64 个时 `CreateSession` 回 `BadTooManySessions`，已建立但未激活的会话也计数。
  释放一个之后可以再建。
- **过期规则**：超过自身 `sessionTimeout` 没有携带任何请求的会话被移除，它名下的全部订阅
  （连同监视项）一起删除。刚刚建立、尚未激活的会话的 `lastActivity` 在创建时就初始化，
  所以不会被第一轮清理误伤。
- **等待应答的会话**（评审复核时发现并补上）：只等通知的客户端发出一个 Publish 请求之后就不再发任何
  请求，直到收到应答；而应答（保活）要到「发布间隔 × 最大保活次数」之后才发出，可能比会话超时还长。
  只按请求判断闲置，会把这样的会话连同订阅一起清掉，而客户端还在等那个应答。打补丁之前会话从不
  过期，所以这是 P7 自己带来的回归。现在 `expireIdle` 跳过「名下至少有一个订阅、且 `PublishRequests`
  里排着请求」的会话：
  - 没有订阅的会话不受保护——它的 Publish 永远不会被应答，否则一个客户端各塞一个请求就能把 64 个
    名额永久占满。
  - 客户端离开之后，排队的请求被保活逐个用掉，订阅随之超时，会话接着过期，保护是有界的。
  - 判断只读会话自己身上的两个字段，都在 broker 的锁内；不涉及订阅服务的锁，锁的顺序不变。
- **锁的顺序**：`DeleteSubscription` 内部要拿 `SubscriptionService.Mu` 和 `MonitoredItemService.Mu`。
  `deleteSubscriptionsOf` 先在 `SubscriptionService.Mu` 之下取一份订阅号的快照，释放后在锁外逐个删除。
- **随 P7 一起处理的三件事**（都是清理协程引入的并发所要求的）：
  1. 原先 `NewSession()` 先把会话登记进表，`CreateSession` 随后在锁外写超时值。清理协程会并发地读它。
     现在超时值在 `NewSession` 里、在锁内设置，会话对外可见时配置就是完整的。
  2. 会话可能在「集中校验通过」和「处理器自己再查一次」之间被清理掉。`CreateMonitoredItems`
     原先拿到 nil 会话会直接解引用，成了一个新的远程崩溃点，现在容忍 nil。
     `CreateSubscription` 把查找放在锁里，保证「会话还在表里时登记的订阅一定会被清理看到」。
  3. `CreateSession` 原先先登记会话、再做可能失败的步骤（签名、随机数），失败时会话留在表里。
     有了上限之后，反复触发失败就能长期占满 64 个名额。现在把这些步骤放在 `NewSession` 之前。
- **上游参考**：v0.9.3 未修（2026-11-26 核对）。
- **测试**：`server/patch_p7_session_limit_test.go`
  - `TestSessionBrokerLimit` — 在 broker 这一层：第 65 个拿不到，关掉一个后能再拿到
  - `TestSessionLimitReached` — 真实客户端连 64 个，第 65 个得到 `BadTooManySessions`
  - `TestIdleSessionExpires` — 带订阅的会话闲置后被清掉，旧令牌失效
  - `TestActiveSessionIsKept` — 持续使用的会话不会被清掉
  - `TestUnactivatedSessionSurvivesUntilTimeout` — 未激活的会话不会提前被清，超时之后才被清
  - `TestNewSessionStartsItsClock` — 新会话的活动时间是创建时刻
  - `TestSweepRetiresIdleSessionWithSubscriptions` — 手动触发一轮清理：闲置会话连同订阅和监视项被清，
    刚用过的会话保留
- **测试**：`server/patch_p7b_waiting_publish_test.go`（等待应答的会话）
  - `TestSweepSparesSessionWaitingOnPublish` — 手动触发清理：排着 Publish 的会话保留，请求没了才清
  - `TestSweepRetiresSessionWaitingWithoutSubscription` — 没有订阅时，排队的 Publish 不构成保护
  - `TestWaitingPublishKeepsSlowSubscriptionSessionAlive` — 真实客户端：会话超时 300 毫秒，订阅的保活
    约 100 秒后才到；只发一个 Publish 并等待，会话与订阅都在，且这段时间里确实没有请求
  - `TestQueuedPublishWithoutSubscriptionStillExpires` — 真实客户端：没有订阅的会话照常过期
  - `TestSessionFollowsItsAbandonedSubscription` — 客户端排一个 Publish 后离开：保活用掉请求、订阅超时、
    会话随后过期
  - 去掉豁免之后，其中两条会失败：`the server holds 0 sessions and 0 subscriptions, want the one
    waiting client and its subscription`
- **未打补丁时的失败表现**：
  ```
  session 65 was handed out although the limit is 64
  the 65th session got <nil>, want ... StatusBadTooManySessions
  timed out waiting for the idle session to be retired                                  （等了 15 秒）
  timed out waiting for the unactivated session to be retired after its timeout         （等了 15 秒）
  ```

### P8　`GetEndpoints` 与 `CreateSession` 的地址匹配

- **改动**：
  - `server/discovery_service.go:63-150`：`GetEndpoints` 改用新增的 `Server.endpointsFor`
    （含 `endpointPort`、`endpointAtHost`）
  - `server/session_service.go:66-69`：`CreateSession` 回应里的 `ServerEndpoints` 也改用 `endpointsFor`
- **调查结论**（任务书要求先确认）：属实，而且比任务书描述的更广。
  未打补丁时，配置为 `opc.tcp://0.0.0.0:4840`：

  | 请求里的地址 | 原代码返回 |
  |---|---|
  | `opc.tcp://192.168.1.10:4840` | 空 |
  | `opc.tcp://localhost:4840` | 空 |
  | `opc.tcp://192.168.1.10`（省略端口） | 空 |
  | `opc.tcp://[::1]:4840` | 空 |
  | 不带地址 | 空 |
  | 与配置串逐字符相同（大小写不敏感） | 正常 |

  `CreateSession` 回应里的端点列表也是同样的逐字符匹配：报配置的地址有 1 项，报主机名是 0 项。
- **问题**：原代码要求请求里的 `EndpointURL` 与配置的端点地址逐字符相等。服务端监听 `0.0.0.0` 时，
  客户端永远发不来这个字符串，于是拿到空的端点列表，很可能连不上。
  OPC UA Part 4 5.4.2 允许服务端按地址过滤，但客户端的地址与监听地址不同是常态。
- **改动内容**：只按 scheme 与端口匹配，主机部分不同也返回端点。返回的是端点的**副本**，
  其 `EndpointURL` 的主机替换成请求里的主机（那是客户端到达服务端的地址）。请求不带地址时原样返回
  配置的端点。没有匹配时仍然返回空数组（与原代码的线上编码一致，不是 null 数组）。
- **测试**：`server/patch_p8_endpoints_test.go`
  - `TestGetEndpointsMatchesByPortNotByHost` — 处理器级，配置 `0.0.0.0:4840` 和 `:4841`，十一个用例
  - `TestGetEndpointsLeavesConfiguredEndpointsAlone` — 回应时改写地址不改动配置的端点对象
  - `TestGetEndpointsOverTheWire` — 真实连接：按 IP 连接，在请求里报 `localhost`；
    本库客户端自己的 `GetEndpoints` 照常
  - `TestCreateSessionListsEndpointsForAnyHost` — 配置 `opc.tcp://127.0.0.1:<port>`，
    客户端用 `localhost` 请求 `CreateSession`，列表非空
  （测试不绑定 `0.0.0.0`：macOS 上监听所有网卡会弹系统授权框。端到端的用例只在请求**字符串**里
  使用别的主机名，连接本身走 IP 字面量，不依赖主机名解析。）
- **上游参考**：v0.9.3 未修（2026-11-26 核对）。
- **未打补丁时的失败表现**：
  ```
  GetEndpoints for "opc.tcp://192.168.1.10:4840" answered [], want [opc.tcp://192.168.1.10:4840]
  GetEndpoints for "opc.tcp://localhost:4840" answered [], want [opc.tcp://localhost:4840]
  GetEndpoints for "" answered [], want [opc.tcp://0.0.0.0:4840 opc.tcp://0.0.0.0:4841]
  GetEndpoints for a host name answered [], want [opc.tcp://localhost:60064]
  CreateSession naming a host name listed [], want [opc.tcp://localhost:60066]
  ```

### P9　订阅号撞号

- **改动**：`server/subscription_service.go` — `SubscriptionService` 上新增 `nextSubID`
  （第 50 行）、`nextSubscriptionID`（第 53-66 行），`CreateSubscription` 改用它取号（第 120 行）。
- **问题**：订阅号按「当前订阅数 + 1」分配。删掉一个再建一个，新订阅会拿到一个还活着的订阅的号，
  直接覆盖表里的那一项：被覆盖的订阅的监视项全部失联，而它的 `run()` 还在跑。
  本库的客户端遇到重号会直接拒绝：`BadSubscriptionIdInvalid`。
- **改动内容**：单调递增的计数器，在已有的 `SubscriptionService.Mu` 之下自增，跳过已占用的号；
  回绕时跳过 0（0 不是合法的订阅号）。
- **上游参考**：v0.9.3 未修（2026-11-26 核对）。
- **测试**：`server/patch_p9_subscription_ids_test.go`
  - `TestSubscriptionIDsAreNotReused` — 建两个、删第一个、再建一个：新号不等于第二个的号，
    第二个仍是它那个号背后的对象
  - `TestSubscriptionIDsAreNotReusedOverTheWire` — 真实客户端和真实推送：同样的序列之后，
    第二个订阅仍能收到通知
- **未打补丁时的失败表现**：
  ```
  the new subscription got 2, the number of the live second subscription
  Subscribe: The subscription id is not valid. StatusBadSubscriptionIDInvalid (0x80280000)
  ```

### P10　`ReverseHello` 让服务端主动外连

- **改动**：`uacp/conn.go:341-352`，服务端握手收到 `RHEF` 时回 `BadTcpEndpointUrlInvalid` 并关闭连接。
  不再解析报文内容，更不拨号。
- **说明**：拒绝点是 UACP 握手层的 `ReverseHello` 报文，**不是** `OpenSecureChannelRequest`。
  本库的 `OpenSecureChannelRequest` 结构体没有 `EndpointUrl` 字段，客户端端点地址放在 UACP 的 `HEL` 报文里。
- **问题**：OPC UA Part 6 的 `ReverseHello` 用于反向连接：配置了反向连接的服务端收到它之后，
  去连接客户端在报文里给出的地址。上游（v0.8.0 与 v0.9.3）在**每一个**监听端口上都接受这个报文，没有任何开关。
  报文里的 `EndpointUrl` 等于服务端配置的监听地址时，服务端先关闭与客户端的连接，
  再拨号到客户端给的 `ServerUri`。
- **风险**：
  - 分支无条件，不需要显式开启。
  - 触发需要 `ReverseHello` 里的 `EndpointUrl` 与配置的监听 URL **字符串完全相等**。
    默认配置 `opc.tcp://0.0.0.0:4840` 可以直接猜到。
  - 后果是**未认证的客户端可诱导网关向任意主机端口发起连接（SSRF）**。
    连上之后服务端只读取、等待对方发来报文头，不发送内容，所以对攻击者而言是盲的；
    但足以对内网做连接探测或触发只认连接的内部服务。
- **上游参考**：v0.9.3 未修（2026-11-26 核对）。
- **探针证据**（未打补丁时，`TestReverseHelloIsRefusedAndNothingIsDialed`，
  诱导目标是测试自己起的本地监听）：
  ```
  the server connected to 127.0.0.1:59382, the address the client supplied
  ```
- **改动理由**：网关不需要反向连接，所以不做配置项，一律拒绝。
- **对客户端的影响**：没有。本库的客户端从不发送 `ReverseHello`。
- **测试**：`server/patch_p10_reversehello_test.go`
  - `TestReverseHelloIsRefusedAndNothingIsDialed` — 两个用例（`EndpointUrl` 等于配置 / 不等于配置）。
    用带超时的 `Accept` 等满 1 秒来证明诱导目标没有收到连接；服务端回
    `BadTcpEndpointUrlInvalid` 并关闭连接
  - `TestClientIsUnaffectedByRefusingReverseHello` — 本库的客户端完整走一遍连接、会话、Browse、
    带推送的订阅

### P11　`CloseSession` 不处理 `DeleteSubscriptions`

- **改动**：`server/session_service.go:141-161`。关闭会话之前先查出会话对象；
  `DeleteSubscriptions` 为 true 时在后台调用 `s.srv.deleteSubscriptionsOf(sess)`。
- **问题**：原代码不读这个标志（留着一行 TODO）。会话没了，订阅还登记在表里，
  连同监视项和发布协程，直到每个订阅各自走完生存期计数。
- **依据**：OPC UA Part 4 5.6.4——标志为 true 时删除该会话名下的全部订阅。
  本库的客户端关会话时发的就是这个标志。
- **复用**：删除逻辑用的就是 P7 清理协程用的 `deleteSubscriptionsOf`，没有第二份实现。
  调用时会话已经不在 broker 里，不可能再有新订阅登记到它名下，所以这次删除能看到属于它的全部订阅。
- **为什么放后台**：和 `DeleteSubscriptions` 处理器的做法一致。删除要拿的锁可能正被一次
  卡住的变更投递持有（见第 6 节），关会话的请求不该等在那上面。
- **标志为 false 时**：行为不变，订阅保持登记。
- **上游参考**：v0.9.3 未修（2026-11-26 核对）。
- **未打补丁时的失败表现**（只关掉这一处，其余补丁保留）：
  ```
  TestCloseSessionDeletesSubscriptionsWhenAsked:  timed out waiting for the subscriptions and monitored items of the closed session to go
  TestClientCloseClearsItsSubscriptions:          timed out waiting for the closed session, its subscription and its monitored item to go
  ```
- **测试**：`server/patch_p11_close_session_test.go`
  - `TestCloseSessionDeletesSubscriptionsWhenAsked` — 两个订阅、各带一个通过真实处理器创建的监视项，
    全部清掉；另一个会话的订阅和监视项不受影响
  - `TestCloseSessionKeepsSubscriptionsWhenNotAsked` — 标志为 false，全部保持登记
  - `TestClientCloseClearsItsSubscriptions` — 本库的客户端 `Close` 之后，服务端不留会话、订阅、监视项

### P12　解码递归没有深度上限

- **改动**：
  - `ua/decode.go:33-78`：常量 `maxNestingDepth`、错误 `errTooDeeplyNested`、接口 `depthAware`、
    分派函数 `decodeBinary`
  - `ua/decode.go:80-85`、`:141`、`:165`、`:226`：`decode`、`decodeStruct`、`decodeSlice`、`decodeArray`
    多一个 `depth` 参数并原样下传，公开的 `Decode` 以 0 调用
  - `ua/buffer.go:38-41`、`:178-199`：`Buffer` 多一个 `depth` 字段，`ReadStruct` 把它交给嵌套值的解码器
  - `ua/variant.go:118-132`、`ua/diagnostic_info.go:33-48`、`ua/extension_object.go:50-85`：
    公开的 `Decode` 以深度 0 调用新增的内部方法 `decodeNested`，超过上限时返回错误而不是继续递归；
    它们里面读到的值深度加一
  - `ua/datatypes.go:39-54`：`DataValue` 也新增 `decodeNested`，但它只是包装，
    里面的 `Variant` 沿用它收到的深度
- **问题**：`Variant` 里可以再套 `Variant`，`DiagnosticInfo` 里可以再套 `DiagnosticInfo`，
  `ExtensionObject` 可以装任何已注册的结构，其中有自己就带 `ExtensionObject` 字段的。
  每嵌套一层就是解码器递归一步，上游没有任何上限（代码里留着 `limit recursion depth to 100` 的待办）。
  Variant 和 DiagnosticInfo 每层在线上只占 1 个字节。
- **触发条件**：一条约 1 MB 的请求，不需要会话。解码发生在 `uasc.SecureChannel.Receive` 里，
  早于 P6 的会话校验。
- **探针证据**（2026-11-26 在未打补丁的上游 v0.9.3 上实测，每个深度各占一个进程）：

  | 嵌套路径 | 每层线上开销 | 表现 |
  |---|---|---|
  | Variant 套 Variant | 1 字节 | 90 万层（900,005 字节）正常返回，解码协程的栈涨到 537,296,896 字节；100 万层 `fatal error: stack overflow` |
  | Variant → DataValue → Variant | 2 字节 | 50 万层正常返回；100 万层（2,000,005 字节，贴着 2 MB 报文上限）栈溢出 |
  | DiagnosticInfo 套 DiagnosticInfo | 1 字节 | 200 万层（2,000,001 字节）仍正常返回，栈涨到 537,264,128 字节，递归无界 |
  | Variant → 扩展对象（`KeyValuePair`，带 Variant 字段）→ Variant | 16 字节 | 12.5 万层（2,000,005 字节）解码成功，栈涨到 537,526,272 字节；25 万层（4 MB）栈溢出 |

  栈溢出是 `fatal error`，`recover` 接不住。TypeId 直接指向 `ExtensionObject` 自己（i=22）的报文不递归：
  注册表里没有这一项，解码到第一层就停。扩展对象的嵌套要经一个已注册的类型，由它带出 Variant 或 ExtensionObject 字段。
- **为什么范围比「两个文件」大**：
  1. 所有嵌套值都经 `Buffer.ReadStruct` 重新进入各自的 `Decode`，而每个手写解码器一进来就新建缓冲区。
     只给 `Variant.Decode` 和 `DiagnosticInfo.Decode` 加深度参数，深度一进 `DataValue` 或
     `ExtensionObject` 就归零。Variant 外面包一层 DataValue 每层只多 1 个字节，
     200 万字节的报文仍在报文上限之内，照样打死进程（上表第二行，探针确认）。
     所以深度必须穿过 `DataValue`、`ExtensionObject` 和 `decode.go` 的反射解码路径传下去。
  2. 生成的类型都是树，没有自引用的结构体，所以解码器里无界的递归只可能经过 `Variant`、
     `DiagnosticInfo`、`ExtensionObject` 这三个手写解码器。把三者都计为一层，这个上限就是
     解码递归的完整上界，不再依赖「ExtensionObject 每层够贵」这个假设。
  3. 深度跟着被解码的数据走。包里没有任何用来计数的变量，并发解码互不影响。
- **上限**：100。一个值外面最多再套 100 层 Variant、DiagnosticInfo、ExtensionObject，`DataValue` 不计。
  OPC UA Part 6 只允许 Variant 嵌套一层，所以对合法报文非常宽松。
- **上游参考**：v0.9.3 未修（2026-11-26 核对）。
- **对客户端可见的影响**：嵌套超过 100 层的报文现在被拒绝，返回
  `opcua: nested more than 100 Variants, DiagnosticInfos or ExtensionObjects deep`。
  合法报文不受影响。这个解码器两个方向共用，客户端面对恶意的服务端同样受保护。
- **测试**：`ua/patch_p12_nesting_test.go`（会打死进程的用例在子进程里跑，见文件里的 `isolated`）
  - `TestDecodeRefusesDeeplyNestedValues` — 五条嵌套路径，每条 100 万层
    （Variant 路径即约 1 MB 的输入），断言返回嵌套错误、进程存活
  - `TestDecodeServiceRefusesDeeplyNestedValue` — 约 1 MB 的 `WriteRequest`，
    走服务端用的入口 `DecodeService`
  - `TestDecodeNestingLimit` — 每条路径的边界：不超上限的最深包装完整解码，再深一层被拒
  - `TestDecodeShallowNestingUnchanged` — **防回归用例**。一到两层合法嵌套用库自己的编码器编码再解码，
    七种形态（Variant 数组、DataValue 的里外、ExtensionObject、DiagnosticInfo 都有），
    结构逐项核对，重新编码得到相同的字节
  - `TestDecodeServiceShallowNestingUnchanged` — 带嵌套值的真实 `WriteRequest`
  - `TestDecodeNestingDepthIsNotShared` — 八个协程并发解码，一个报文的深度不影响另一个

  `server/patch_p12_server_test.go`
  - `TestServerSurvivesOverDeepNesting` — 真实客户端经 TCP 发嵌套 150 层的 Write：
    服务端不回应这条请求，下一个客户端照常得到服务
- **未打补丁时的失败表现**（换基线当天在 v0.9.3 上确认，与 v0.8.0 上相同）：
  ```
  Variant in Variant / Variant in DataValue in Variant:
      exit status 2
      runtime: goroutine stack exceeds 1000000000-byte limit
      fatal error: stack overflow
  WriteRequest（约 1 MB）:             同上，栈溢出
  DiagnosticInfo、101 层边界、并发用例:  An error is expected but got nil.
  TestServerSurvivesOverDeepNesting:   the server answered a value nested 150 levels deep
                                       with *ua.WriteResponse (service result StatusGood)
  ```
  两个「浅嵌套不变」的测试在未打补丁的代码上通过，打补丁后仍然通过。

### P13　`FindServers` 不检查端点列表

- **改动**：`server/discovery_service.go:31-39`。`Endpoints()` 为空时返回空列表，不再取第一项。
- **问题**：服务端的端点来自 `EnableSecurity` 启用的安全策略。一个也没启用时，服务端照样监听和应答，
  但没有端点。原代码直接取 `Endpoints()[0].Server`，一个 `FindServers` 请求就是请求协程上的越界 panic。
- **触发条件**：需要「没启用任何安全策略」这个配置错误。网关启用了 `None`，走不到这里。
  这条是顺手堵一个明确的越界，不是一个现成的洞。
- **返回什么**：空列表加 `StatusGood`，与同文件里 `GetEndpoints` 在没有匹配时的做法一致。
- **上游参考**：v0.9.3 未修（2026-11-26 核对）。
- **测试**：`server/patch_p13_findservers_test.go`
  - `TestFindServersWithoutEndpoints` — 处理器级；回应可以编码，再解码回来
  - `TestFindServersWithoutEndpointsOverTheWire` — 监听中的服务端，经客户端库询问
- **未打补丁时的失败表现**：
  ```
  panic: runtime error: index out of range [0] with length 0
      server/discovery_service.go:31
  ```

## 4. 已评估、无需修改

**`decodeArray`**（`ua/decode.go:226-278`）
与 `decodeSlice` 不同，它先检查 `n > uint32(val.Len())`（第 241 行），超出目标数组长度的声明直接拒绝；
分配的大小由数组类型决定，从不取自报文。另外，编解码类型里没有任何定长数组的字段，
它从报文根本走不到。`TestDecodeArrayLengthIsBoundedByArrayType` 钉住这个结论。

**`Variant` 里的数组**（`ua/variant.go:17`、`:157`）
元素个数受 `MaxVariantArrayLength`（65535）限制，预分配是有界的；解码在第一个出错的地方就终止，
所以一条报文最多造成一次这样的分配。和 P3 不是一个量级的问题。

**编码器的递归同样没有上限**
`ua/encode.go` 里把一个值编码出来也是递归的，嵌套多深就递归多深。它编码的是本地构造的值，
或者从报文解出来的值，后者受 P12 约束，最多 100 层。所以没有从报文到达的触发路径。

**排除的误报：「Browse 卡死服务端」**
P6 的测试第一次运行时，Browse 之后的请求全部超时，看起来像服务端的请求协程死锁了。
把进程的协程栈打出来看，请求协程空闲地等在 `ReadMessage` 上，根本没有卡住；
真正发生的是那条 Browse 让服务端丢弃了这条连接——测试构造的请求里 `View` 是 nil，
而本库的编码器对 nil 指针不写任何字节，解码器却总是读完整的结构（见第 5 节第 1 条）。
不是服务端的死锁。

**P3 的检查不会误拒合法报文**
编解码类型里每个数组元素在线上至少占 1 个字节（自定义 `Decode` 的类型全部如此；
没有零字节的元素类型）。上游 `ua` 包自带的全部编解码往返测试和
`TestDecodeArrayLengthUpToInputSize` 的边界用例都通过。

**报文与分片的大小限制不按报文头预分配**
`uacp.Conn.Receive` 分配的是固定 64 KB 的缓冲，分片按实际收到的字节累积，上限 512 片，
合并后再检查 2 MB。内存与实际收到的数据成正比，没有放大。

**下标 0 的漏删分支在标准 ns0 里走不到**
见 P2。已用合成命名空间钉住。

**`Subscription.Update`**（`server/subscription_service.go:369`）不做钳制
它只从 `ModifyChannel` 被调用，而库里没有任何地方往这个通道写；`ModifySubscription` 服务返回不支持。
它也不重建定时器。当前不可达。

**网关的测试套件分不出库有没有打补丁**
网关套件对未改动的上游 v0.8.0 和 v0.9.3 同样全部通过（v0.9.3 于 2026-11-26 实测，13 个包）。
适配层 `opcuaserver/gopcua/service_guard.go` 在网关一层做了会话校验、间隔钳制、监视项校验和 panic 兜底。
它能证明补丁没有带来回归，不能证明补丁生效。补丁的回归防线是本仓库的 `patch_*_test.go`。

## 5. 只读审计清单：未修改的问题

以下问题在补丁过程中发现，**没有修改**。每条注明证据等级：
「探针确认」是用一次性的探针实际触发了；「只读码」是读代码得出的。

清单在 `sagooiot-v0.8.0` 分支上有 14 条。原第 2 条和原第 9 条上游在 v0.9.3 里修了，这里删去，后面的编号比那边小 2：

- **编码器遇到 nil 指针会 panic**（原第 2 条）。通用编码器 `ua/encode.go:45-47` 先看类型有没有自己的 `Encode()`、
  有就直接调用，这一步排在 `:79` 的 nil 指针判断之前，所以 nil 的 `*NodeID`、`*Variant` 会带着 nil 接收者进 `Encode()`。
  v0.9.3 给 `ua` 包里全部 9 个这样的类型加了 nil 判断，编码成各自的空值。
  2026-11-26 探针确认：9 个类型的 `(*T)(nil).Encode()` 都不再 panic；`ViewID` 为 nil 的 `BrowseRequest`
  编码再解码正常。
- **`Node.SetAttribute` 设置成功却返回错误**（原第 9 条）。v0.9.3 改成一律返回 `nil`（`server/node.go:205-220`）。

1. **编码器与解码器对 nil 指针字段不对称** — `ua/encode.go:78-81` 对 nil 指针不写任何字节；
   `ua/decode.go:151-153` 却总是分配并读取完整的结构。带这种字段的消息发出去之后无法解码。
   - **探针确认**：`BrowseRequest.View` 为 nil 时，服务端解码失败并丢弃这条连接，
     客户端之后的请求全部超时（见第 4 节的误报说明）。
   - **只读码**：没有任何处理器应答这类消息；目前只有测试自己构造的请求触发过。

2. **`ActivateSession` 不校验用户身份** — `server/session_service.go:90-128`。
   匿名、用户名密码、证书，任何令牌都被接受；`EnableAuthMode` 只影响端点列出的令牌策略。
   这是授权上的缺口。**只读码。**（网关只启用 `None` 加匿名，并在文档里写明了。）

3. **非 RSA 证书导致 panic** — `uasc/secure_channel_crypto.go:25`、`:51`、`:80`、`:117`，
   `remoteX509Cert.PublicKey.(*rsa.PublicKey)` 是没有保护的类型断言。
   仅在启用 `None` 以外的安全策略时可达，发生在建会话之前。**只读码。**
   （`uasc/secure_channel.go:577`、`:773`、`:951` 的同类断言是带 `ok` 的。）

4. **会话令牌的取值空间只有 31 位** — `server/session_broker.go:88`，`mrand.Int31()`。
   撞号时会悄悄顶掉原先持有该令牌的会话。**只读码。**

5. **`DeleteMonitoredItem` 会漏删** — `server/monitored_item_service.go:41-48`：
   `Req` 或 `NodeID` 为 nil、或者该节点在 `Nodes` 里没有条目时直接返回，不从表里删。
   **探针确认**（P7 的测试夹具不完整时留下过 2 个监视项）；通过处理器创建的监视项不会触发。

6. **监视项处理器不核对请求里的 `SubscriptionID`** — 只有 `CreateMonitoredItems` 读它
   （`server/monitored_item_service.go:176`）。`SetMonitoringMode` 与 `DeleteMonitoredItems`
   只要监视项属于本会话就接受，不管它属于哪个订阅。Part 4 要求此时返回 `Bad_MonitoredItemIdInvalid`。
   所有权检查（P5）覆盖了安全面。**只读码。**

7. **`IncludeSubtypes=false` 仍然接受其他所有子类型** — `server/view_service.go:153-172`。
    规范要求只返回 exactly 那一种引用类型；本库只额外排除了 `HasSubtype`。
    P2 保持了这个既有语义，没有改。**只读码。**

8. **`Server.Node` 不加锁读 `s.namespaces`** — `server/server.go:449-453`；`AddNamespace` 在锁内追加。
    启动之后再添加命名空间会有数据竞争。**只读码。**

9. **每个会话排队超过 100 个 Publish 请求时，多出来的被悄悄丢弃** —
    `server/subscription_service.go` 的 `Publish` 处理器（`default` 分支只打一条警告），
    客户端只能等到超时。**只读码。**

10. **订阅协程发送响应时没有写超时** — `server/subscription_service.go` 里
    `SendResponseWithContext(context.Background(), ...)`。不再读取的订阅者会无限期卡住这个协程，
    这也是第 6 节那个问题的前半段。**只读码。**

11. **畸形的报文让服务端悄悄断开这条连接** — `server/channel_broker.go:102-113`：
    接收或解码出任何错误就停止读取，不回 ERR 或 `ServiceFault`，客户端看到的是超时。
    **探针确认**（同第 1 条）。Part 6 期望在可能时回一个错误响应。
    另外 `RegisterConn` 退出时并不关闭这条连接的套接字，全靠客户端来关（`server/channel_broker.go:54-125`）。
    **只读码。**

12. **上游既有的测试问题**：`ua/buffer.go:71`、`:249` 触发 `go vet` 的 `stdmethods`；
    `uacp.TestResolveEndpoint` 依赖真实 DNS。见第 0 节。

## 6. 已知遗留

**变更通知在持锁状态下阻塞发送。** `server/monitored_item_service.go:100-135`，
`ChangeNotification` 持有 `MonitoredItemService.Mu` 期间向 `item.Sub.NotifyChannel`（容量 100）发送
（第 127、132 行）。订阅的 `run()` 不在接收状态时——它卡在向一个不再读取的订阅者发送响应——
通道填满后，调用 `Server.ChangeNotification` 的那个协程会永远阻塞，并一直持有这把锁。

- **探针确认**（用一个没有启动 `run()` 的订阅模拟这个状态）：
  ```
  ChangeNotification still blocked after 500ms: the notification channel (cap 100) is full and nobody drains it
  DeleteSubscription blocked as well after 300ms; the subscription service mutex is held by it: true
  ```
- **后果**：`DeleteSubscription` 先拿 `SubscriptionService.Mu` 再等 `MonitoredItemService.Mu`，
  所以它也卡住，并且一直持有 `SubscriptionService.Mu`。此后任何订阅类请求都会阻塞。
  在库里，`SetAttribute` 是同步调用 `ChangeNotification` 的，卡住的会是唯一的请求处理协程，
  整个服务端停止响应。P7 的清理协程和 P11 的后台删除都可能卡在同一条路径上。
- **现状**：库层面没有修。网关在适配层用异步投递隔离了它
  （`opcuaserver/gopcua/delivery.go`，带阻塞检测和关闭时的等待上限）。

## 7. 对客户端可见的行为变化

相对上游 v0.9.3：

| 补丁 | 变化 |
|---|---|
| P2、P4、P5(a) | 原先让进程退出的请求现在得到正常的错误结果 |
| P4(b) | `RevisedPublishingInterval` 落在 `[100ms, 1h]` 内 |
| P5(b) | 对别的会话的监视项做删除或改模式，得到 `BadSessionIdInvalid`，且不再生效 |
| P6 | 没有有效会话的请求得到 `ServiceFault(BadSessionIdInvalid)` |
| P7 | 闲置超过自身超时的会话过期，连同它的订阅；会话数上限 64。名下有订阅、且在等 Publish 应答的会话不算闲置 |
| P8 | 任何主机名的地址都能拿到端点列表，返回的 URL 带客户端使用的主机 |
| P9 | 订阅号不再复用 |
| P10 | `ReverseHello` 被拒绝 |
| P11 | `CloseSession(DeleteSubscriptions=true)` 删除会话名下的订阅 |
| P12 | 嵌套超过 100 层 Variant、DiagnosticInfo、ExtensionObject 的报文被拒绝（解码错误），合法报文不受影响 |
| P13 | 没有端点的服务端对 `FindServers` 回空列表，不再越界 |

P1、P3、P7a 的行为由上游 v0.9.3 提供，本分支没有改动。和 `sagooiot-v0.8.0` 分支相比只有两处不同：

- P1：向未注册的命名空间 Write 得到 `BadNodeIDUnknown`（那边是 `BadNotInView`）。
- P7a：会话超时的上限是 1 小时（那边是 30 分钟）。

## 8. 与上游同步

1. 取回上游的新版本，把本分支移到新的标签上：`git fetch upstream --tags`，然后在
   `sagooiot-v0.9.3` 分支里 `git rebase v0.x.y`，逐条解决冲突。`go.mod`、`go.sum` 以**上游的**为准。
   （`upstream` 指向 `https://github.com/gopcua/opcua.git`。）
2. 第 3 节的补丁和全部 `patch_*` 测试随提交一起带过去。
   `git diff v0.x.y..sagooiot-v0.9.3 -- <文件>` 可以直接看到某一处相对上游的全部改动。
3. 上游修掉某一条之后，撤掉本分支对它的改动，保留它的测试并确认仍然通过；上游的边界与我们的不同时，
   把测试改成上游的边界，在第 3 节写明。P1、P3、P7a 在 v0.9.3 上就是这样处理的。
4. 逐条撤掉补丁，确认针对它的测试变红。整份文件还原成上游不一定可行，几条补丁共用标识符；
   2026-11-26 换基线时，P2、P4、P5、P9、P10、P12、P13 还原文件就够了；P6、P7、P8、P11 与别的补丁共用标识符，
   是逐个关掉行为验证的。
5. 第 3 到 6 节的行号要按新基线重排，改完把每个范围的首尾行对着源码核一遍。
   只按首尾两行的文字对位不可靠：`}` 和空行到处都是，范围会被对到别的函数上；
   上游改了函数体时，文字对位也发现不了。2026-11-26 换基线时审计清单的原第 2、9 条就是这样漏掉的，
   靠逐个读上游的 diff 才发现。
6. 每条补丁的测试用到了库的内部标识符
   （`suitableRefType`、`getSubRefs`、`sessionBroker`、`maxSessions`、`sessionlessServices`、
   `sessionSweepInterval` 等），上游改名后需要跟着调整。
7. 第 5 节的问题上游可能已经修了，同步时逐条复查，修了的从清单里划掉。
8. 2026-11-26 核对过上游主干（未发版的默认分支 `main`）：`view_service.go`、`ua/variant.go`、`uacp/conn.go`、
   `subscription_service.go` 等文件与 v0.9.3 相同，上面 11 项都还没有修。短期内等不到上游。
9. P12 改动了解码器的内部调用链：`decode`、`decodeStruct`、`decodeSlice`、`decodeArray` 多了一个深度参数，
   `Buffer` 多了一个字段，四个手写解码器各多了一个内部方法 `decodeNested`。上游动过这些函数的话，
   这一条要手工合并，不能只按行号套用。

## 3.x P14 — ActivateSession 用户名口令校验

**问题**：`ActivateSession` 只校验会话签名并轮换 nonce，从不读取 `UserIdentityToken`。
端点即使声明了 Username、或关掉了 Anonymous，任何客户端仍能建会话。

**修复**：
- 新增 `server.UserAuth(fn)`：在 `ActivateSession` 里解析 `AnonymousIdentityToken` /
  `UserNameIdentityToken`；未启用的令牌类型 → `BadIdentityTokenRejected`；
  用户名口令经回调校验失败 → `BadUserAccessDenied`。
- `uasc.SecureChannel.DecryptUserPassword`：按 Part 4 布局（length + password + serverNonce）
  解密口令；必须在轮换 serverNonce **之前**校验。
- 端点 `UserIdentityTokens` 仍由 `EnableAuthMode` 驱动：未启用 Anonymous 则不声明匿名策略。

**测试**：`server/patch_p14_username_auth_test.go`、`uasc/decrypt_user_password_test.go`。

## 9. 使用方怎么更新

使用方在自己的 `go.mod` 里用 `replace` 钉在本仓库的**标签**上：

```go.mod
replace github.com/gopcua/opcua => github.com/xinjiayu/opcua v0.9.3-sagooiot.2
```

标签就是发布点：`v<上游基线>-sagooiot.<N>` 同时写明上游基线和补丁轮次，每个标签对应第 3 节的一套
补丁。这个版本号会记进使用方的二进制（`go version -m <二进制>`），现场排查时能直接看出用的是哪一版。

| 本仓库变了什么 | 维护者 | 使用方 |
|---|---|---|
| 代码 | 打新标签 `v0.9.3-sagooiot.N` 并推送 | `replace` 改到新标签，`GOWORK=off go mod tidy` |
| 只改文档 | 只推分支，不打标签 | 不用动 |
| 使用方想试用还没打标签的提交 | — | 临时用伪版本号，验证完换回标签 |

```sh
# 打标签并推送
git tag -a v0.9.3-sagooiot.N -m "SagooIoT 安全补丁第 N 版（v0.9.3 基线）：……"
git push origin v0.9.3-sagooiot.N

# 把分支头解析成伪版本号（临时试用用）
go list -m -json github.com/xinjiayu/opcua@sagooiot-v0.9.3
```

- **伪版本号只用来临时试用**，不作长期依赖。它是一长串提交号，看不出是第几轮补丁；分支上每多一个
  提交（哪怕只改了文档）都会诱使使用方跟着改 `go.mod`。
- **不要把发布过的标签挪到新提交上。** Go 的模块版本是不可变的：下载过旧内容的机器会报校验和
  不匹配，模块代理也继续返回旧内容。
- 新版本（标签或伪版本号）的校验和由 sum.golang.org 现算，它要能访问 GitHub。它连不上的那段时间，
  使用方的构建会因为校验不过而失败，表现为校验和查询返回 404。稍后重试即可，不要为此关掉校验。
