package keystore

import "crypto/rand"

// randRead 是对 crypto/rand.Read 的封装，方便测试替换 randReader。
func randRead(b []byte) (int, error) {
	return rand.Read(b)
}
