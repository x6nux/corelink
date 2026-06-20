package splittunnel

import "testing"

// buildTestClientHello 构造一个最小合法的 TLS 1.2 ClientHello，包含指定域名的 SNI 扩展。
func buildTestClientHello(serverName string) []byte {
	sniBytes := []byte(serverName)
	sniLen := len(sniBytes)

	// --- SNI 扩展 ---
	// ServerName entry: type(1) + length(2) + name
	snEntry := []byte{0x00} // host_name type
	snEntry = append(snEntry, byte(sniLen>>8), byte(sniLen))
	snEntry = append(snEntry, sniBytes...)

	// ServerNameList: length(2) + entries
	snList := []byte{byte(len(snEntry) >> 8), byte(len(snEntry))}
	snList = append(snList, snEntry...)

	// Extension: type(2) + length(2) + data
	ext := []byte{0x00, 0x00} // server_name extension type
	ext = append(ext, byte(len(snList)>>8), byte(len(snList)))
	ext = append(ext, snList...)

	// Extensions 总长度
	extensions := []byte{byte(len(ext) >> 8), byte(len(ext))}
	extensions = append(extensions, ext...)

	// --- ClientHello body ---
	var body []byte

	// client_version: TLS 1.2 = 0x0303
	body = append(body, 0x03, 0x03)

	// random: 32 字节
	body = append(body, make([]byte, 32)...)

	// session_id: length=0
	body = append(body, 0x00)

	// cipher_suites: length=2, 一个套件 TLS_RSA_WITH_AES_128_GCM_SHA256 (0x009c)
	body = append(body, 0x00, 0x02, 0x00, 0x9c)

	// compression_methods: length=1, null(0x00)
	body = append(body, 0x01, 0x00)

	// extensions
	body = append(body, extensions...)

	// --- Handshake header ---
	var hs []byte
	hs = append(hs, 0x01) // ClientHello type
	// handshake length: 3 字节
	hs = append(hs, byte(len(body)>>16), byte(len(body)>>8), byte(len(body)))
	hs = append(hs, body...)

	// --- TLS Record ---
	var record []byte
	record = append(record, 0x16)       // ContentType: Handshake
	record = append(record, 0x03, 0x01) // Version: TLS 1.0（record 层通常使用 1.0）
	// record length: 2 字节
	record = append(record, byte(len(hs)>>8), byte(len(hs)))
	record = append(record, hs...)

	return record
}

func TestSniffTLS_ValidSNI(t *testing.T) {
	hello := buildTestClientHello("example.com")
	sni := SniffTLS(hello)
	if sni != "example.com" {
		t.Fatalf("期望 example.com, got %q", sni)
	}
}

func TestSniffTLS_NotTLS(t *testing.T) {
	if sni := SniffTLS([]byte("GET / HTTP/1.1\r\n")); sni != "" {
		t.Fatalf("非 TLS 应返回空, got %q", sni)
	}
}

func TestSniffTLS_NoSNI(t *testing.T) {
	// ClientHello without SNI extension
	if sni := SniffTLS([]byte{0x16, 0x03, 0x01}); sni != "" {
		t.Fatalf("无 SNI 应返回空, got %q", sni)
	}
}

func TestSniffHTTP_ValidHost(t *testing.T) {
	req := []byte("GET / HTTP/1.1\r\nHost: example.com\r\nConnection: close\r\n\r\n")
	host := SniffHTTP(req)
	if host != "example.com" {
		t.Fatalf("期望 example.com, got %q", host)
	}
}

func TestSniffHTTP_PostRequest(t *testing.T) {
	req := []byte("POST /api HTTP/1.1\r\nHost: api.example.com\r\nContent-Length: 0\r\n\r\n")
	host := SniffHTTP(req)
	if host != "api.example.com" {
		t.Fatalf("期望 api.example.com, got %q", host)
	}
}

func TestSniffHTTP_NotHTTP(t *testing.T) {
	if host := SniffHTTP([]byte{0x16, 0x03, 0x01}); host != "" {
		t.Fatalf("非 HTTP 应返回空, got %q", host)
	}
}

func TestSniffHTTP_HostWithPort(t *testing.T) {
	req := []byte("GET / HTTP/1.1\r\nHost: example.com:8080\r\n\r\n")
	host := SniffHTTP(req)
	// 返回包含端口的完整 Host 值
	if host != "example.com:8080" {
		t.Fatalf("期望 example.com:8080, got %q", host)
	}
}
