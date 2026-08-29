---
name: doris-query
status: in-use
---

# 在 Doris 上查询电池遥测数据

## When To Use

需要从 `bms_telemetry` / `charge_session` / `vehicle_model` 取数时。

## Procedure

1. 先确认口径：要哪个车型、哪个时间范围、哪些工况。
2. 查 `/agents/data/*/derived/` 下的派生小表确认范围是否存在。
3. 只在范围确定后对主表做一次带过滤条件的聚合查询。
4. 结论中标注所用表与时间范围。

## Known Pitfalls

- `bms_telemetry` 按 `dt` 分区，缺少 `dt` 过滤会全表扫描。
- 权限类报错（compute group / backend）不是 SQL 语法问题，改写 SQL 无用。
