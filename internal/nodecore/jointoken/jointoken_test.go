package jointoken

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestEncodeProducesBase64URLOfJSON(t *testing.T) {
	tk := JoinToken{
		V: 1,
		H: "controller.example.com",
		K: "0123456789abcdef0123456789abcdef",
		C: "sha256:" + "ab" + repeat62("c"),
	}
	s, err := Encode(tk)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("结果应为 base64url(无填充): %v", err)
	}
	var got JoinToken
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("内层应为合法 JSON: %v", err)
	}
	if got != tk {
		t.Fatalf("往返不一致: got %+v want %+v", got, tk)
	}
}

// repeat62 返回 s 重复 62 次的辅助串，用于凑足 64 hex 字符。
func repeat62(s string) string {
	out := ""
	for i := 0; i < 62; i++ {
		out += s
	}
	return out
}

func validToken() JoinToken {
	return JoinToken{
		V: 1,
		H: "controller.example.com",
		K: "0123456789abcdef0123456789abcdef",
		C: "sha256:" + "a" + repeat62("a") + "a", // 64 个 hex
	}
}

func TestDecodeRoundTrip(t *testing.T) {
	tk := validToken()
	s, err := Encode(tk)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := Decode(s)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got != tk {
		t.Fatalf("往返不一致: got %+v want %+v", got, tk)
	}
}

func TestDecodeRejectsInvalid(t *testing.T) {
	mut := func(f func(*JoinToken)) string {
		tk := validToken()
		f(&tk)
		s, _ := Encode(tk)
		return s
	}
	cases := []struct {
		name string
		in   string
	}{
		{"非base64url", "!!!not-base64!!!"},
		{"非JSON", mustBase64("not json")},
		{"版本不为1", mut(func(t *JoinToken) { t.V = 2 })},
		{"版本为0", mut(func(t *JoinToken) { t.V = 0 })},
		{"host为空", mut(func(t *JoinToken) { t.H = "" })},
		{"key为空", mut(func(t *JoinToken) { t.K = "" })},
		{"ca为空", mut(func(t *JoinToken) { t.C = "" })},
		{"ca缺sha256前缀", mut(func(t *JoinToken) { t.C = "a" + repeat62("a") + "a" })},
		{"ca前缀错误", mut(func(t *JoinToken) { t.C = "sha1:" + "a" + repeat62("a") + "a" })},
		{"ca hex不足64", mut(func(t *JoinToken) { t.C = "sha256:abcd" })},
		{"ca含非hex字符", mut(func(t *JoinToken) { t.C = "sha256:" + "g" + repeat62("g") + "g" })},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Decode(c.in); err == nil {
				t.Fatalf("非法输入 %q 应返回 error", c.name)
			}
		})
	}
}

func mustBase64(s string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(s))
}
