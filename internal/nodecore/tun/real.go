package tun

import (
	"fmt"
	"os"
	"sync"

	wgtun "golang.zx2c4.com/wireguard/tun"
)

// realTUN 包装外部 wireguard-go tun 包的真实 TUN 设备。
//
// wgtun.Device 的方法集与本地 Device 基本一致，realTUN 做薄适配，
// 把内嵌的 wgtun.Device 暴露为本地接口类型（便于上层统一依赖）。
type realTUN struct {
	dev     wgtun.Device
	events  <-chan Event   // 缓存的事件桥接通道，避免每次 Events() 调用都创建新 goroutine
	closeCh chan struct{}  // 关闭信号，用于退出事件桥接 goroutine
	wg      sync.WaitGroup // 等待事件桥接 goroutine 退出
	once    sync.Once      // 保证 closeCh 仅关闭一次
}

var _ Device = (*realTUN)(nil)

// CreateReal 创建真实 TUN 设备。
//
// 注意：真实 TUN 创建需要特权（root / CAP_NET_ADMIN），仅应在运行期调用，
// 不在单元测试中调用。
func CreateReal(name string, mtu int) (Device, error) {
	dev, err := wgtun.CreateTUN(name, mtu)
	if err != nil {
		return nil, fmt.Errorf("tun: 创建真实 TUN %q 失败（需特权）: %w", name, err)
	}
	// 在创建时启动事件桥接 goroutine，缓存通道，避免 Events() 重复调用导致泄漏
	ch := make(chan Event, 4)
	closeCh := make(chan struct{})
	t := &realTUN{dev: dev, events: ch, closeCh: closeCh}
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		defer close(ch)
		for {
			select {
			case e, ok := <-dev.Events():
				if !ok {
					return
				}
				select {
				case ch <- Event(e):
				case <-closeCh:
					return
				}
			case <-closeCh:
				return
			}
		}
	}()
	return t, nil
}

func (t *realTUN) File() *os.File { return t.dev.File() }

func (t *realTUN) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	return t.dev.Read(bufs, sizes, offset)
}

func (t *realTUN) Write(bufs [][]byte, offset int) (int, error) {
	return t.dev.Write(bufs, offset)
}

func (t *realTUN) MTU() (int, error)     { return t.dev.MTU() }
func (t *realTUN) Name() (string, error) { return t.dev.Name() }
func (t *realTUN) BatchSize() int        { return t.dev.BatchSize() }

// Close 关闭 TUN 设备并等待事件桥接 goroutine 退出。
func (t *realTUN) Close() error {
	t.once.Do(func() {
		close(t.closeCh)
	})
	t.wg.Wait()
	return t.dev.Close()
}

// Events 返回缓存的事件桥接通道（在 CreateReal 中一次性创建）。
func (t *realTUN) Events() <-chan Event {
	return t.events
}
