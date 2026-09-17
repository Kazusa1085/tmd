# Twitter Media Downloader

[![Go Reference](https://pkg.go.dev/badge/github.com/unkmonster/tmd.svg)](https://pkg.go.dev/github.com/unkmonster/tmd)
[![Go Report Card](https://goreportcard.com/badge/github.com/unkmonster/tmd)](https://goreportcard.com/report/github.com/unkmonster/tmd)
[![Coverage Status](https://coveralls.io/repos/github/unkmonster/tmd/badge.svg?branch=master)](https://coveralls.io/github/unkmonster/tmd?branch=master)
[![Go](https://github.com/unkmonster/tmd/actions/workflows/go.yml/badge.svg)](https://github.com/unkmonster/tmd/actions/workflows/go.yml)
![GitHub Release](https://img.shields.io/github/v/release/unkmonster/tmd) 
![GitHub License](https://img.shields.io/github/license/unkmonster/tmd?logo=github)

跨平台的推特媒体下载器。用于轻松，快速，安全，整洁，批量的下载推特上用户的推文。支持手动指定用户或通过列表、用户关注批量下载。开箱即用！

## Feature

- 下载指定用户的媒体推文 (video, img, gif)
- 保留推文标题
- 保留推文发布日期，设置为文件的修改时间
- 以列表为单位批量下载
- 关注中的用户批量下载
- 在文件系统中保留列表/关注结构
- 同步用户/列表信息：名称，是否受保护，等。。。
- 记录用户曾用名
- 避免重复下载
  - 每次工作后记录用户的最新发布时间，下次工作仅从这个时间点开始拉取用户推文
  - 向列表目录发送指向用户目录的符号链接，无论多少列表包含同一用户，本地仅保存一份用户存档
- 避免重复获取时间线：任意一段时间内的推文仅仅会从 twitter 上拉取一次，即使这些推文下载失败。如果下载失败将它们存储到本地，以待重试或丢弃
- 避免重复同步用户（更新用户信息，获取时间线，下载推文）
- 速率限制：避免触发 Twitter API 速率限制
- 自动关注受保护的用户
- 添加备用 cookie：提高推文获取速度和总数量

## How to use

### 下载/编译

**直接下载**

前往 [Release](https://github.com/unkmonster/tmd/releases/latest) 自行选择合适的版本并下载

**自行编译**

```bash
git clone https://github.com/unkmonster/tmd
cd tmd
go build .
```

### 更新/填写配置

第一次运行程序时，程序会询问如下配置信息，请按要求将配置项依次填入

#### 配置项介绍

1. `root_path`：媒体存储路径(可以不存在)。引导程序里显示为 `enter storage dir`
2. `auth_token`：用于登录，[获取方式](https://github.com/unkmonster/tmd/blob/master/doc/help.md#获取-cookie)
3. `ct0`：用于登录，[获取方式](https://github.com/unkmonster/tmd/blob/master/doc/help.md#获取-cookie)
4. `max_download_routine`：最大并发下载协程数（如果为0取默认值）
5. `state_path`（可选）：程序自身状态（数据库、失败重试队列）的存放目录，
   默认 `<root_path>/.data`

> **`state_path` 建议单独指定到本地磁盘。** 如果你的媒体放在 NAS 共享
> （NFS/SMB）上，这个目录不要跟着放：SQLite 依赖可靠的文件锁，在网络共享上
> 会出错甚至损坏数据库。媒体可以放共享，程序状态放本地卷。

配置目录默认是 `$HOME/.tmd2/`（Windows 为 `%appdata%\.tmd2\`），
可用环境变量 `TMD_CONFIG_DIR` 覆盖——容器部署时用来把配置挂载到外部目录。

#### 更新配置

```shell
tmd --conf
```

> **执行上述命令将导致引导配置程序重新运行，这将重新配置整个配置文件，而不是单独的配置项。单独修改配置项**请至 `%appdata%/.tmd2/conf.yaml` 或 `$HOME/.tmd2/conf.yaml`手动修改

### 命令说明

```
tmd --help                 // 显示帮助
tmd --conf                 // 重新运行配置程序
tmd --user <user_id>       // 下载由 user_id 指定的用户的推文
tmd --user <screen_name>   // 下载由 screen_name 指定的用户的推文
tmd --list <list_id>       // 批量下载由 list_id 指定的列表中的每个用户
tmd --foll <user_id>       // 批量下载由 user_id 指定的用户正关注的每个用户
tmd --foll <screen_name>   // 批量下载由 screen_name 指定的用户正关注的每个用户
tmd --targets <path>       // 从文件读取要下载的账号列表（默认 <配置目录>/targets.yaml）
tmd --auto-follow          // 自动关注受保护的用户
tmd --no-retry             // 仅转储，不在程序退出前自动重试下载失败的推文
```

### 批量下载多个用户

把要下载的账号写进 `<配置目录>/targets.yaml`，然后直接运行 `tmd`：

```yaml
users:
  - 44196397          # 数字 ID，推荐：账号改名也不会找错人
  - "@elonmusk"       # handle，需要加引号
```

也可以继续用 `--user` 写在命令行上，两者会合并并自动去重。

**列表里有账号被封、注销或不可用时，不会中断整个任务**：该账号会被跳过并记录，
其余账号照常下载。每次运行后在配置目录生成 `targets_report.tsv`，逐条记录结果：

```
handle     user_id   status   reason        media         detail           last_error
@elonmusk  44196397  ok                     new_media=42
someone    1234      skipped  suspended                    user unavailable
another    5678      failed   rate_limited                                    ...
```

- `status`：`ok` 成功 / `skipped` 预期内跳过 / `failed` 意外失败
- `reason`：`suspended`（被封或注销）、`protected`（受保护且未关注）、
  `blocked`（被你屏蔽或静音）、`rate_limited`、`auth`、`network`、`unknown`

### 退出码

| 退出码 | 含义 |
|---|---|
| `0` | 全部成功，或只有预期内的跳过 |
| `1` | 出现意外失败（网络、限流、写入错误） |
| `2` | 参数或配置错误 |
| `3` | 所有目标都因认证失败 → 大概率是 cookie 失效 |

这样任务计划器只在真正需要时报警，不会因为"某个账号被封了"天天打扰你。

### 文件在磁盘上长什么样

每个用户一个目录，**每条推文一个子目录**（子目录名是推文 ID）：

```
<存储路径>/users/<用户名>/
└── 1839204812/              推文 ID
    ├── 01.jpg               附件，按相册原始顺序，零填充
    ├── 02.mp4
    ├── caption.txt          推文正文（纯 UTF-8）
    └── meta.json            推文 ID / 链接 / 发布时间 / 作者 / 附件清单
```

因此：

- 文件名**不含推文文本**：长推文不会撞上文件名长度上限，重名也不会产生 `(1)` 副本；
- **同一条推文重试会覆盖自己的文件**，不会重复下载出多余副本；
- `meta.json` 是**最后写入的**，它存在即代表这条推文已完整落盘，可据此判断完整性；
- 正文与元数据都在盘上，归档是自描述的，以后重建索引、去重或喂给
  Immich / PhotoPrism 之类工具都不需要重新联网。


> 为了创建符号链接，在 Windows 上应该以管理员身份运行程序

[不知道啥是 user_id/list_id/screen_name?](https://github.com/unkmonster/tmd/blob/master/doc/help.md#%E8%8E%B7%E5%8F%96-list_id-user_id-screen_name)

### 示例

```
tmd --user elonmusk  // 下载 screen_name 为 ‘eronmusk’ 的用户
tmd --user 1234567   // 下载 user_id 为 1234567 的用户
tmd --list 8901234   // 下载 list_id 为 8901234 的列表
tmd --foll 567890    // 下载 user_id 为 567890 的用户正关注的所有用户
```

更推荐的做法：一次运行

```shell
tmd --user elonmusk --user 1234567 --list 8901234 --foll 567890
```

### 设置代理

运行前通过环境变量指定代理服务器（TUN 模式跳过这一步）

```bash
set HTTP_PROXY=url
set HTTPS_PROXY=url
```

示例：
```bash
set HTTP_PROXY=http://127.0.0.1:7890
set HTTPS_PROXY=http://127.0.0.1:7890
tmd --user elonmusk
```

如果你使用windows系统，在powershell中使用以下指令设置代理：
```powershell
$Env:HTTP_PROXY="http://127.0.0.1:7890"
$Env:HTTPS_PROXY="http://127.0.0.1:7890"
```

### 忽略用户

程序默认会忽略被静音或被屏蔽的用户，所以当你想要下载的列表中包含你不想包含的用户，可以在推特将他们屏蔽或静音

### 添加额外 cookie

程序动态从所有可用 cookie 中选择一个不会被速率限制的 cookie 请求用户推文，以避免因单一 cookie 的速率限制导致程序被阻塞。

按如下格式创建 `$HOME/.tmd2/additional_cookies.yaml` 或 `%appdata%/.tmd2/additional_cookies.yaml`

```yaml
- auth_token: xxxxxxxxx1
  ct0: xxxxxxxxxxxxxxxxxxxxxxx
- auth_token: xxxxxxxxx2
  ct0: xxxxxxxxxxxxxxxx2
- auth_token: xxxxxxxxxxxxxxxx3
  ct0: xxxxxxxxxxxxxxxxxxxxx3
```
> 这些添加的备用 cookie，仅用来提升获取推文的速率和总量。判断是否忽略用户和自动关注受保护的用户依然使用主账号

## Docker / NAS 部署

镜像一次性运行：下载、写报告、退出。**定时任务交给 NAS 的任务计划器或系统 cron**，
不要让容器常驻（程序本身跑完即退出，常驻需要额外处理 PID 1、cron 日志、时区）。

```bash
docker build -t tmd .

# 手动跑一次
docker run --rm \
  -e TZ=Asia/Shanghai \
  -v /volume1/docker/tmd/config:/config \
  -v /volume1/docker/tmd/state:/state \
  -v /volume1/media/tmd:/data \
  tmd
```

`docker/entrypoint.sh` 会在启动时检查配置是否挂载、目录是否可写，并给出可操作的
提示，而不是等到后面报一个和真正原因无关的错误。

三个挂载点的分工：

| 挂载点 | 内容 | 建议位置 |
|---|---|---|
| `/config` | `conf.yaml`、`targets.yaml`、`additional_cookies.yaml`、日志、报告 | 容器配置目录 |
| `/state` | 数据库、失败重试队列 | **本地卷**（SQLite 需要可靠文件锁） |
| `/data` | 媒体文件 | 可以放共享，`conf.yaml` 里 `root_path` 指向它 |

`config/` 目录下有完整的示例配置与 compose 文件。相关环境变量：`TMD_CONFIG_DIR`
（配置目录，镜像里默认 `/config`）、`TZ`（日志时区）。

## Detail

### 关于速率限制

Twitter API 限制一段时间内过快的请求 （例如某端点每15分钟仅允许请求500次，超出这个次数会以429响应），当某一端点将要达到速率限制程序会打印一条通知并阻塞尝试请求这个端点的协程直到余量刷新（这最多是15分钟），但并不会阻塞所有协程，所以其余协程打印的消息可能将这条休眠通知覆盖让人认为程序无响应了，等待余量刷新程序会继续工作。

## Contributors

![](https://contrib.rocks/image?repo=unkmonster/tmd) 

## 交流群

tg: https://t.me/+I4yyM81HaJpkNTll

## 感谢

本项目 CDN 加速及安全防护由 Tencent EdgeOne 赞助：EdgeOne 提供长期有效的免费套餐，包含不限量的流量和请求，覆盖中国大陆节点，且无任何超额收费，感兴趣的朋友可以点击下面的链接领取

<a href="https://edgeone.ai/zh?from=github">亚洲最佳CDN、边缘和安全解决方案 - Tencent EdgeOne</a>

<img src="https://edgeone.ai/media/34fe3a45-492d-4ea4-ae5d-ea1087ca7b4b.png" alt="图片alt" title="图片title">


