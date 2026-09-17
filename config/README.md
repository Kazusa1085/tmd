# 配置与部署示例

> ✅ **这些示例对应当前实现。**
> `--targets`、`targets.yaml`、`targets_report.tsv`、`TMD_CONFIG_DIR`、
> `state_path`、退出码都已落地并经过实跑验证。
> 部署方式见仓库根的 `readme.md` 与 `Dockerfile`、`docker/entrypoint.sh`。

## 文件说明

| 文件 | 作用 | 复制成 |
|---|---|---|
| `conf.example.yaml` | 主配置:存储路径、凭据、并发数 | `conf.yaml` |
| `targets.example.yaml` | **目标列表**:要爬哪些人 | `targets.yaml` |
| `additional_cookies.example.yaml` | 可选:备用 cookie,提升拉取速度 | `additional_cookies.yaml` |
| `docker-compose.example.yml` | NAS/Docker 部署 | `docker-compose.yml` |

## 三个目录的分工

```
<config>/    conf.yaml、targets.yaml、additional_cookies.yaml、日志、报告
<root_path>/ 媒体文件 ← 你真正在乎的数据,可以放 NAS 共享
<state_path>/ foo.db、errors.json ← 程序状态,建议放本地卷
```

**为什么状态要单独放**:SQLite 依赖可靠的文件锁,在网络共享(NFS/SMB/CIFS)
上会出错甚至损坏数据库。把媒体和状态分开,SQLite 就永远不会碰到网络文件系统。

## 在 NAS 上部署

```bash
# 1) 建目录
mkdir -p /volume1/docker/tmd/{config,state}
mkdir -p /volume1/media/tmd

# 2) 放配置(用你的 UID,避免权限问题)
id -u; id -g
cp config/conf.example.yaml       /volume1/docker/tmd/config/conf.yaml
cp config/targets.example.yaml    /volume1/docker/tmd/config/targets.yaml
chmod 600 /volume1/docker/tmd/config/*.yaml

# 3) 编辑 conf.yaml 填 cookie,编辑 targets.yaml 填要爬的人

# 4) 跑一次
docker compose run --rm tmd
```

定时运行交给 NAS 的任务计划器(不是容器内部 cron),命令就是
`docker compose run --rm tmd`。理由见 `docker-compose.example.yml` 里的注释。

## 目标列表(targets.yaml)

程序会**逐个**处理列表里的人。关键行为:

- **任何一个人失败都不会中断其余的。** 账号被封、注销、受保护、被你屏蔽,
  都会被跳过并记录,然后继续下一个。
- 失败清单写到 `<config>/targets_report.tsv`(每次运行重写)。
- 原来也有的 `--user` / `--list` / `--foll` 参数**继续可用**,会与列表合并。

### 两种写法

推荐(简洁):

```yaml
users:
  - 44196397        # 数字 ID,不加引号 = 账号 ID,最稳
  - "@elonmusk"     # handle,加引号
```

显式(更严谨):

```yaml
users:
  - id: 44196397
  - handle: elonmusk
```

> **为什么要强调引号**:YAML 会把不带引号的 `yes` / `no` / `on` / `off`
> 解析成布尔值。一个叫 `on` 的账号会被读成 `true`。加引号就没这个问题。
> 数字 ID 则相反,不加引号表示"这是 ID 不是名字"。

## 失败报告(targets_report.tsv)

制表符分隔,表头固定:

```
handle	user_id	status	reason	last_error
elonmusk	44196397	ok	new_media=42	
someone	1234	skipped	suspended	user unavailable: __typename is UserUnavailable
```

### status 取值

| status | 含义 | 是否计入"意外失败" |
|---|---|---|
| `ok` | 成功(含"没有新推文") | 否 |
| `skipped` | 预期内的跳过 | 否 |
| `failed` | 意外失败,值得排查 | **是** |

### reason 取值

| reason | 含义 | 典型 status |
|---|---|---|
| `suspended` | 账号被封禁 | skipped |
| `not_found` | 账号已注销或不存在 | skipped |
| `protected` | 受保护且未关注(可配 `--auto-follow`) | skipped |
| `blocked` | 被你屏蔽或静音 | skipped |
| `rate_limited` | 触发速率限制 | failed |
| `network` | 网络/超时错误 | failed |
| `io` | 磁盘写入失败(空间不足、权限) | failed |

### 退出码

| 退出码 | 条件 |
|---|---|
| `0` | 全部成功,或**只有**预期内的跳过 |
| `1` | 出现了 `failed` 类记录 |
| `2` | 参数/配置错误 |
| `3` | 疑似 cookie 失效(全部目标均失败) |

> 退出码是给你接监控用的:任务计划器可以据此报警,
> 而"某人被封了"这种正常的跳过不会天天吵你。

## 已落地的决定

1. **数据库**:`journal_mode=DELETE`(不用 WAL),`busy_timeout` 30 秒,
   DSN 做 URI 转义,错误必须可见不能静默吞。
2. **状态目录**:`state_path` 可配,默认 `root_path/.data`(向后兼容)。
3. **配置目录**:`TMD_CONFIG_DIR` 环境变量覆盖,默认 `$HOME/.tmd2`。
4. **TZ**:镜像内置 tzdata,支持 `TZ`。
5. **权限**:不做 PUID/PGID 脚本,用 compose 的 `user:`;但启动时做一次
   可写性自检,失败时明确提示当前 UID 和宿主机排查方法。
6. **容器**:一次性任务,不内置 cron。
7. **推文目录布局**(媒体侧):
   ```
   <root_path>/users/<用户目录>/<推文ID>/
       01.jpg         按相册原始顺序,零填充
       caption.txt    纯正文,UTF-8
       meta.json      id/url/created_at/author/account/media[]
   ```
   `meta.json` **最后写**,作为"这条推文下载完整"的标记。
8. **登录校验**:从页面 `__INITIAL_STATE__` 的 `session.user_id` 判定是否已登录,
   不靠正则抓 `"screen_name"`(匿名页面里也有该字段,会导致假 cookie 被当成登录成功)。
9. **目标失败分类**:`suspended` / `protected` / `blocked` 属预期内跳过(退出码 0),
   `rate_limited` / `auth` / `network` / `unknown` 计为意外失败(退出码 1;
   全部为 `auth` 时退出码 3)。
