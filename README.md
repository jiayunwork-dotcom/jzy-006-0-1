# 宇宙学红移与哈勃距离核算服务

一个**纯服务端、可独立部署**的计算组件：上游把光谱观测数据交给它，计算红移量、退行速度、
低红移共动距离，并判定观测是否落在非相对论线性区。无前端、无账户体系。

- 语言：Go（标准库 `net/http`，仅用 `lib/pq` 连接 PostgreSQL）
- 持久化：外部数据库 PostgreSQL（另附零依赖内存实现用于本地/测试）
- 部署：`docker compose up --build` 一键启动服务与数据库

## 固定单位（全链路统一，不允许混用）

| 量 | 单位 |
|---|---|
| 波长（静止/观测） | nm |
| 退行速度 | km/s |
| 共动距离 | Mpc（兆秒差距） |
| 哈勃常数 H0 | km/s/Mpc |
| 光速常量 | c = 299792.458 km/s |

每个响应都带 `units` 字段显式声明这些量纲。

## 物理定义与两套关系

- 红移定义：`z = λ_obs / λ_rest - 1`，**分母是静止波长**。
  - λ_obs > λ_rest → z > 0，红移；λ_obs < λ_rest → z < 0，蓝移；相等 → z = 0。
  - 关键判据：静止波长不变、观测波长加倍时，新红移 `z' = 2z + 1`，而非 `2z`。
- 默认宇宙学线性关系：`v = c·z`，`d = v/H0`。
- 相对论多普勒关系（可选开关 `relativistic=true`）：
  `v = c·((1+z)²−1)/((1+z)²+1)`；由速度反推 z 时 `z = sqrt((1+β)/(1−β)) − 1`。
  两套关系相互独立，**默认绝不使用相对论公式**。
- 线性区阈值固定为 `z ≤ 0.1`。超过阈值仍返回线性近似数值，但显式置
  `outside_linear=true` 并附 `warning`，提示结果不再可靠。
- 蓝移（z<0）的距离查询**不会**返回负的“前方距离”，而是返回 HTTP 422 结构化错误，
  错误体显式携带 `"blueshift": true` 与负的红移值。
- 静止波长、观测波长、H0 取非正值（或 NaN/Inf）一律在计算前返回结构化错误。

## 预置算例（约 3% 红移，H0=70）

`λ_rest=500 nm → λ_obs=515 nm`：

- z = 0.03
- v（线性）= 8993.77374 km/s
- d = 8993.77374 / 70 ≈ **128.48 Mpc**

启动后访问 `GET /api/v1/example` 可直接取得该算例及相对论对照值。

## HTTP 接口

基址 `/api/v1`，请求/响应均为 JSON。

### `POST /redshift` — 计算红移（与速度）

三种输入模式任选其一：波长对、直接给 `redshift`、或直接给 `velocity_km_s`。
默认线性关系下速度按 `z = v/c` 反演；`relativistic=true` 时按相对论多普勒反演
`z = sqrt((1+β)/(1−β)) − 1`（要求 |v|<c）。

```json
{ "rest_wavelength_nm": 500, "observed_wavelength_nm": 515 }
```

### `POST /distance` — 计算距离与速度并判定线性区

```json
{ "rest_wavelength_nm": 500, "observed_wavelength_nm": 515, "h0_km_s_mpc": 70 }
```

成功响应（节选）：

```json
{
  "redshift": 0.03,
  "classification": "redshift",
  "blueshift": false,
  "velocity_km_s": 8993.77374,
  "distance_mpc": 128.482482,
  "h0_km_s_mpc": 70,
  "relation": "cosmological_linear",
  "relativistic": false,
  "linear_regime": true,
  "outside_linear": false,
  "units": { "wavelength": "nm", "velocity": "km/s", "distance": "Mpc", "h0": "km/s/Mpc" }
}
```

打开相对论开关：请求中加 `"relativistic": true`，`relation` 变为 `relativistic_doppler`。

### `POST /batch` — 批量计算

顶层 `h0_km_s_mpc` 作为默认值，可被每行覆盖；`relativistic` 为全局开关。
每行独立给出 `ok` + `result` 或结构化 `error`，互不影响。

```json
{
  "h0_km_s_mpc": 70,
  "lines": [
    { "line_id": "A", "rest_wavelength_nm": 500, "observed_wavelength_nm": 515 },
    { "line_id": "B", "redshift": 0.2 },
    { "line_id": "C", "rest_wavelength_nm": 500, "observed_wavelength_nm": 490 }
  ]
}
```

### `GET /history` — 历史查询（成功与失败请求均持久化）

过滤参数（均可组合）：`request_id`、`kind=redshift|distance`、`blueshift=true|false`、
`outside_linear`、`success`、`from`/`to`（RFC3339）、`limit`、`offset`。
结果按时间倒序。每个请求带 `X-Request-ID`（可由调用方指定，缺省自动生成），
响应头原样回显。

### 监控

- `GET /health/live` — 存活探针
- `GET /health/ready` — 就绪探针（会 ping 数据库）

### 结构化错误

```json
{ "error": { "code": "NON_POSITIVE_HUBBLE_CONSTANT", "message": "H0 must be ..." } }
```

错误码：`NON_POSITIVE_REST_WAVELENGTH`、`NON_POSITIVE_OBSERVED_WAVELENGTH`、
`NON_POSITIVE_HUBBLE_CONSTANT`、`MISSING_FIELD`、`AMBIGUOUS_INPUT`、
`INVALID_NUMBER`、`INVALID_JSON`、`INVALID_QUERY_PARAMETER`、
`BLUESHIFT_DISTANCE_QUERY`（HTTP 422，带 `blueshift:true`）、`PERSISTENCE_ERROR`。

## 运行

一键启动（服务 + PostgreSQL）：

```bash
docker compose up --build
curl -s localhost:8080/api/v1/example
```

零外部依赖本地运行（内存存储，重启即清空）：

```bash
go run ./cmd/server                 # 默认 STORE_BACKEND=postgres
STORE_BACKEND=memory PORT=8080 go run ./cmd/server
```

连接已有 PostgreSQL：设置 `DATABASE_URL` 或 `PGHOST/PGPORT/PGUSER/PGPASSWORD/PGDATABASE`。
表结构在启动时自动创建（`CREATE TABLE IF NOT EXISTS`）。

## 测试

```bash
go test -race -count=1 ./...
```

覆盖的关键行为：

1. 红移定义：观测波长加倍 ⇒ `z' = 2z+1`（非简单翻倍），且分母为静止波长；
2. 同一 z 下 H0 加倍 ⇒ 距离减半（d 与 H0 成反比）；
3. z=0 ⇒ v=0、d=0；
4. z>0.1 ⇒ `outside_linear=true` + 显式 warning；
5. 蓝移距离查询 ⇒ 422 + `blueshift:true`，绝不返回负距离；
6. 相对论开关与默认线性关系互不混用（数值不同、`relation` 不同、速度反演仅在开关打开时允许）；
7. 批量逐行计算、成功/失败各自正确、全部独立持久化、历史过滤正确；
8. 80 路并发请求互不串扰、每请求恰好落库一次（`-race` 通过）；
9. 非正波长/H0、缺字段、多义输入、未知 JSON 字段等结构化错误。

对真实 PostgreSQL 的集成测试在提供 `TEST_DATABASE_URL` 时启用：

```bash
TEST_DATABASE_URL='host=localhost port=5432 user=cosmo password=cosmo dbname=cosmo sslmode=disable' \
  go test ./internal/store/
```

## 项目结构

```
cmd/server/             服务入口（存储后端选择、优雅关停）
internal/cosmology/     纯物理计算（红移、两套速度关系、线性距离、阈值、校验）
internal/store/         持久化接口 + PostgreSQL / 内存两种实现
internal/api/           HTTP 路由、请求校验、批量、历史、健康检查、中间件
Dockerfile, docker-compose.yml
```
