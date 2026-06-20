// Package tun 提供 CoreLink 的 TUN 设备抽象。
//
// Device 接口定义了 TUN 设备的标准操作集。提供两种实现：
//
//   - realTUN：包装 wireguard-go tun.CreateTUN 的真实设备。创建真实 TUN
//     需要特权（root/CAP_NET_ADMIN），仅在运行期调用，不在单测中创建。
//   - fakeTUN：纯内存双向队列实现，供单元测试使用（无需特权）。
//
// 之所以包一层而非直接用外部 tun.Device，是为了让上层（DataPlane、cmd/）
// 依赖本地接口、便于在测试中注入 fakeTUN。
package tun

import (
	"errors"
	"os"
)

// ErrClosed 表示设备已关闭。
var ErrClosed = errors.New("tun: 设备已关闭")

// Event 表示 TUN 设备事件类型。
type Event int

const (
	EventUp        Event = 1 << iota // 设备启动
	EventDown                        // 设备关闭
	EventMTUUpdate                   // MTU 变更
)

// Device 是 CoreLink 使用的 TUN 设备接口。
type Device interface {
	// File 返回底层文件描述符（fakeTUN 返回 nil）。
	File() *os.File

	// Read 从设备读取一个或多个包（不含额外头部）。成功时返回读到的包数，
	// 并把各包长度写入 sizes。offset 指示从每个 buf 的何处开始写入。
	Read(bufs [][]byte, sizes []int, offset int) (n int, err error)

	// Write 向设备写入一个或多个包。offset 指示从每个 buf 的何处开始读取。
	Write(bufs [][]byte, offset int) (int, error)

	// MTU 返回设备 MTU。
	MTU() (int, error)

	// Name 返回设备名。
	Name() (string, error)

	// Events 返回设备事件通道。
	Events() <-chan Event

	// Close 停止设备并关闭事件通道。
	Close() error

	// BatchSize 返回单次读写的首选/最大包数。
	BatchSize() int
}
