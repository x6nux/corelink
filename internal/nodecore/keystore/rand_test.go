package keystore

import (
	"testing"
)

// TestRandRead_填充随机字节 验证 randRead 能正确填充指定长度的字节切片。
func TestRandRead_填充随机字节(t *testing.T) {
	buf := make([]byte, 32)
	n, err := randRead(buf)
	if err != nil {
		t.Fatalf("randRead: %v", err)
	}
	if n != 32 {
		t.Fatalf("randRead 返回 %d 字节, 期望 32", n)
	}
	// 验证不全为零（密码学随机源极不可能产生 32 字节全零）
	allZero := true
	for _, b := range buf {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Fatal("randRead 产生全零，极不可能——随机源有问题")
	}
}

// TestRandRead_不同调用产生不同结果 验证两次调用产生不同的随机字节。
func TestRandRead_不同调用产生不同结果(t *testing.T) {
	buf1 := make([]byte, 32)
	buf2 := make([]byte, 32)
	if _, err := randRead(buf1); err != nil {
		t.Fatalf("第一次 randRead: %v", err)
	}
	if _, err := randRead(buf2); err != nil {
		t.Fatalf("第二次 randRead: %v", err)
	}
	same := true
	for i := range buf1 {
		if buf1[i] != buf2[i] {
			same = false
			break
		}
	}
	if same {
		t.Fatal("两次 randRead 产生相同结果，概率极低——随机源有问题")
	}
}

// TestRandRead_零长度 验证零长度切片不报错。
func TestRandRead_零长度(t *testing.T) {
	buf := make([]byte, 0)
	n, err := randRead(buf)
	if err != nil {
		t.Fatalf("零长度 randRead: %v", err)
	}
	if n != 0 {
		t.Fatalf("零长度 randRead 返回 %d, 期望 0", n)
	}
}

// TestRandRead_多种长度 表驱动验证不同长度的读取。
func TestRandRead_多种长度(t *testing.T) {
	lengths := []int{1, 16, 32, 64, 128, 256}
	for _, length := range lengths {
		t.Run("", func(t *testing.T) {
			buf := make([]byte, length)
			n, err := randRead(buf)
			if err != nil {
				t.Fatalf("randRead(%d): %v", length, err)
			}
			if n != length {
				t.Fatalf("randRead(%d) 返回 %d", length, n)
			}
		})
	}
}

// TestRandReader_与randRead一致 验证包级 randReader 使用 randRead。
func TestRandReader_与randRead一致(t *testing.T) {
	buf := make([]byte, 16)
	n, err := randReader.Read(buf)
	if err != nil {
		t.Fatalf("randReader.Read: %v", err)
	}
	if n != 16 {
		t.Fatalf("randReader.Read 返回 %d, 期望 16", n)
	}
	// 不应全零
	allZero := true
	for _, b := range buf {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Fatal("randReader 产生全零")
	}
}
