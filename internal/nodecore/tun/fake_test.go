package tun

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

// TestFakeTUN_接口合规 验证 fakeTUN 满足 Device 接口。
func TestFakeTUN_接口合规(t *testing.T) {
	var _ Device = (*fakeTUN)(nil)
}

// TestFakeTUN_构造参数 验证 NewFakeTUN 返回的设备名、MTU、BatchSize 正确。
func TestFakeTUN_构造参数(t *testing.T) {
	tests := []struct {
		name    string
		tunName string
		mtu     int
	}{
		{"默认", "utun0", 1420},
		{"大MTU", "utun-big", 9000},
		{"小MTU", "tun1", 576},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dev := NewFakeTUN(tt.tunName, tt.mtu)
			defer dev.Close()
			if name, _ := dev.Name(); name != tt.tunName {
				t.Errorf("Name() = %q, 期望 %q", name, tt.tunName)
			}
			if mtu, _ := dev.MTU(); mtu != tt.mtu {
				t.Errorf("MTU() = %d, 期望 %d", mtu, tt.mtu)
			}
			if bs := dev.BatchSize(); bs != 1 {
				t.Errorf("BatchSize() = %d, 期望 1", bs)
			}
		})
	}
}

// TestFakeTUN_File返回nil 验证 fakeTUN 的 File() 返回 nil。
func TestFakeTUN_File返回nil(t *testing.T) {
	dev := NewFakeTUN("utun-file", 1420)
	defer dev.Close()
	if f := dev.File(); f != nil {
		t.Fatalf("fakeTUN.File() 应返回 nil, got %v", f)
	}
}

// TestFakeTUN_Events 验证创建后立即收到 EventUp。
func TestFakeTUN_Events(t *testing.T) {
	dev := NewFakeTUN("utun-ev", 1420)
	defer dev.Close()
	select {
	case ev := <-dev.Events():
		if ev != EventUp {
			t.Fatalf("首个事件 = %v, 期望 EventUp", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("超时等待 EventUp")
	}
}

// TestFakeTUN_Inject深拷贝 验证 Inject 对输入数据做了深拷贝，修改原切片不影响设备内数据。
func TestFakeTUN_Inject深拷贝(t *testing.T) {
	dev := NewFakeTUN("utun-cp", 1420)
	defer dev.Close()
	data := []byte("original")
	dev.Inject(data)
	// 修改原切片
	data[0] = 'X'
	bufs := [][]byte{make([]byte, 64)}
	sizes := make([]int, 1)
	n, err := dev.Read(bufs, sizes, 0)
	if err != nil || n != 1 {
		t.Fatalf("Read: n=%d, err=%v", n, err)
	}
	if got := bufs[0][:sizes[0]]; !bytes.Equal(got, []byte("original")) {
		t.Fatalf("Inject 未做深拷贝：Read 得到 %q", got)
	}
}

// TestFakeTUN_Write深拷贝 验证 Write 对写入数据做了深拷贝。
func TestFakeTUN_Write深拷贝(t *testing.T) {
	dev := NewFakeTUN("utun-wcp", 1420)
	defer dev.Close()
	data := []byte("writedata")
	n, err := dev.Write([][]byte{data}, 0)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != 1 {
		t.Fatalf("Write n=%d, 期望 1", n)
	}
	// 修改原切片
	data[0] = 'Z'
	select {
	case got := <-dev.Outbound():
		if !bytes.Equal(got, []byte("writedata")) {
			t.Fatalf("Write 未做深拷贝：Outbound 得到 %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("超时等待 Outbound")
	}
}

// TestFakeTUN_WriteOffset 验证 Write 的 offset 语义。
func TestFakeTUN_WriteOffset(t *testing.T) {
	dev := NewFakeTUN("utun-wo", 1420)
	defer dev.Close()
	// 前 4 字节为头部填充，实际内容从 offset=4 开始
	buf := make([]byte, 20)
	copy(buf[4:], "payload-data")
	n, err := dev.Write([][]byte{buf}, 4)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != 1 {
		t.Fatalf("Write n=%d", n)
	}
	select {
	case got := <-dev.Outbound():
		if !bytes.Equal(got, buf[4:]) {
			t.Fatalf("Write offset 语义错误：got %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("超时等待 Outbound")
	}
}

// TestFakeTUN_Write多包 验证 Write 可同时写多个包。
func TestFakeTUN_Write多包(t *testing.T) {
	dev := NewFakeTUN("utun-multi", 1420)
	defer dev.Close()
	bufs := [][]byte{[]byte("pkt-a"), []byte("pkt-b"), []byte("pkt-c")}
	n, err := dev.Write(bufs, 0)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != 3 {
		t.Fatalf("Write n=%d, 期望 3", n)
	}
	for _, want := range []string{"pkt-a", "pkt-b", "pkt-c"} {
		select {
		case got := <-dev.Outbound():
			if string(got) != want {
				t.Errorf("Outbound 得到 %q, 期望 %q", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("超时等待 %q", want)
		}
	}
}

// TestFakeTUN_Write_OffsetBeyondLen 验证 offset 超出 buf 长度时不写入。
func TestFakeTUN_Write_OffsetBeyondLen(t *testing.T) {
	dev := NewFakeTUN("utun-off", 1420)
	defer dev.Close()
	shortBuf := []byte("ab")
	// offset=10 超出 buf 长度，应跳过
	n, err := dev.Write([][]byte{shortBuf}, 10)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != 0 {
		t.Fatalf("offset 超出时 Write n=%d, 期望 0", n)
	}
}

// TestFakeTUN_Close幂等 验证多次 Close 不报错。
func TestFakeTUN_Close幂等(t *testing.T) {
	dev := NewFakeTUN("utun-idem", 1420)
	if err := dev.Close(); err != nil {
		t.Fatalf("第一次 Close: %v", err)
	}
	if err := dev.Close(); err != nil {
		t.Fatalf("第二次 Close: %v", err)
	}
}

// TestFakeTUN_关闭后Write返回错误 验证关闭后 Write 返回 ErrClosed。
func TestFakeTUN_关闭后Write返回错误(t *testing.T) {
	dev := NewFakeTUN("utun-cw", 1420)
	dev.Close()
	_, err := dev.Write([][]byte{[]byte("test")}, 0)
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("关闭后 Write err = %v, 期望 ErrClosed", err)
	}
}

// TestFakeTUN_关闭后Inject忽略 验证关闭后 Inject 不 panic（静默忽略）。
func TestFakeTUN_关闭后Inject忽略(t *testing.T) {
	dev := NewFakeTUN("utun-ci", 1420)
	dev.Close()
	// 不应 panic
	dev.Inject([]byte("ignored"))
}
