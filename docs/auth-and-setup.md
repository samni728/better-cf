# 管理员登录与初始化

## 目标

WebUI 会保存 Cloudflare API Token、Zone ID、目标域名、扫描参数，并且后续会执行 DNS 删除和创建操作。因此第一版必须有管理员登录保护。

MVP 不做多用户系统，只做单管理员：

```text
第一次访问 -> 创建管理员 -> 登录 -> 配置参数 -> 执行任务
```

## 首次初始化流程

当本地配置中不存在管理员时：

```text
GET /
-> 跳转 /setup
-> 用户填写管理员账号和密码
-> 后端校验并写入本地状态
-> 跳转 /login
```

初始化页面字段：

```text
用户名
密码
确认密码
```

规则：

- 用户名不能为空。
- 密码不能为空。
- 两次密码必须一致。
- 管理员一旦存在，禁止再次访问初始化写入。

## 登录流程

管理员存在后：

```text
GET /
-> 未登录跳转 /login
-> 登录成功进入 Dashboard
```

登录成功后创建 HttpOnly Cookie。

第一版会话可以先保存在内存中，服务重启后需要重新登录。后续再扩展到数据库会话表。

## 配置流程

登录后进入配置页面，填写：

```text
Cloudflare API Token
Cloudflare Account ID
Cloudflare Zone ID
目标完整域名
IPv4 写入数量
IPv6 写入数量
协议是否 TLS
设置带宽 Mbps
RTT 并发数
定时运行时间
```

Cloudflare API Token 不应在页面上明文回显。编辑时可以留空表示保留旧值。

## 权限边界

第一版只有两种状态：

```text
未登录
已登录管理员
```

未登录只能访问：

```text
/setup
/login
/healthz
```

已登录管理员才能访问：

```text
/
/dashboard
/settings
/run
/history
```

## 安全原则

- 密码不能明文存储。
- API Token 不能在 WebUI 明文回显。
- 删除 DNS 记录必须只针对配置的完整域名和 A / AAAA 类型。
- 第一版先不开放公网访问更安全，建议先通过 VPS IP + 非标准端口或 SSH 隧道测试。

## 后续增强

Next 阶段可以加入：

- 修改管理员密码。
- 登录失败次数限制。
- 会话持久化。
- HTTPS 反向代理。
- API Token 加密存储。
