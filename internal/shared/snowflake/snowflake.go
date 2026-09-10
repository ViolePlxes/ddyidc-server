package snowflake

import (
	"sync/atomic"
	"time"
)

// sequence is kept below 4096 so the low bits can safely distinguish IDs
// generated during the same millisecond.
var sequence atomic.Int64

func Init(workerId int64) {
	// 保留初始化入口，后续接入分布式 worker ID 时不影响调用方。
}

// GenID 生成雪花ID
// 实际项目中使用 go-zero 的 snowflake 节点
// 这里提供统一入口，方便后续替换
func GenID() int64 {
	return time.Now().UnixMilli()<<12 | sequence.Add(1)&0x0fff
}
