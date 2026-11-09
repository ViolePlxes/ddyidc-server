# 离线地域库

`ip2region_v4.xdb` 为 ip2region 官方 IPv4 离线数据库，编译时嵌入服务端二进制；未配置
`VisitAnalytics.GeoIPFile` 或该文件不可读时使用本副本。

- 来源：https://github.com/lionsoul2014/ip2region （`data/ip2region_v4.xdb`）
- 结构：xdb v3 + IPv4 + 向量索引
- 大小：11,122,742 字节
- SHA-256：`a12ad5aca1277d67e525efcda531a20cc0d654ff4f087150d45bc17fca4bc260`
- 许可：Apache-2.0（保留上游版权与许可声明，不修改数据内容）

更新流程：

1. 从上游下载新的 `ip2region_v4.xdb`。
2. 替换本文件并更新上面的 SHA-256，重新构建发布。
3. 也可以不改二进制，直接把文件放到 `Runtime.DataDir/storage/geoip/` 并配置
   `VisitAnalytics.GeoIPFile`，服务会按 `GeoIPReloadMinutes` 热加载（外部文件优先）。

校验：`sha256sum internal/visitanalytics/data/ip2region_v4.xdb`。
