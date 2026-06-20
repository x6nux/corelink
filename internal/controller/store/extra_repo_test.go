package store

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// ---------- ListLeases ----------

func TestListLeases(t *testing.T) {
	s := newMemStore(t)
	if err := s.AllocateLease("100.64.0.2", "n1"); err != nil {
		t.Fatal(err)
	}
	if err := s.AllocateLease("100.64.0.3", "n2"); err != nil {
		t.Fatal(err)
	}
	leases, err := s.ListLeases()
	if err != nil {
		t.Fatal(err)
	}
	if len(leases) != 2 {
		t.Fatalf("got %d leases, want 2", len(leases))
	}
}

func TestListLeasesEmpty(t *testing.T) {
	s := newMemStore(t)
	leases, err := s.ListLeases()
	if err != nil {
		t.Fatal(err)
	}
	if len(leases) != 0 {
		t.Fatalf("got %d leases, want 0", len(leases))
	}
}

// ---------- ListNodes ----------

func TestListNodes(t *testing.T) {
	s := newMemStore(t)
	if err := s.CreateNode(&Node{ID: "a", Role: "node", WGPubKey: "pk1", VirtualIP: "100.64.0.2"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNode(&Node{ID: "b", Role: "node", WGPubKey: "pk2", VirtualIP: "100.64.0.3"}); err != nil {
		t.Fatal(err)
	}
	nodes, err := s.ListNodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 {
		t.Fatalf("got %d nodes, want 2", len(nodes))
	}
}

// ---------- EnrollKey ----------

func TestGetEnrollKeyNotFound(t *testing.T) {
	s := newMemStore(t)
	_, err := s.GetEnrollKey("nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestCreateAndGetEnrollKey(t *testing.T) {
	s := newMemStore(t)
	exp := time.Now().Add(time.Hour)
	ek := &EnrollKey{
		Key:       "secret-key-1",
		Reusable:  false,
		ExpiresAt: &exp,
		Tag:       "user:alice",
	}
	if err := s.CreateEnrollKey(ek); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetEnrollKey("secret-key-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Tag != "user:alice" {
		t.Fatalf("tag = %q, want user:alice", got.Tag)
	}
	if got.Revoked {
		t.Fatal("key should not be revoked")
	}
}

func TestRevokeEnrollKey(t *testing.T) {
	s := newMemStore(t)
	if err := s.CreateEnrollKey(&EnrollKey{Key: "k1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeEnrollKey("k1"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetEnrollKey("k1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Revoked {
		t.Fatal("key should be revoked")
	}
}

// TestConsumeOneTimeKey_Basic 单次消费：第一次返回 true 并吊销，第二次返回 false。
func TestConsumeOneTimeKey_Basic(t *testing.T) {
	s := newMemStore(t)
	if err := s.CreateEnrollKey(&EnrollKey{Key: "once"}); err != nil {
		t.Fatal(err)
	}
	ok, err := s.ConsumeOneTimeKey("once")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("首次消费应返回 true")
	}
	// key 已被消费（consumed=true），但未被管理员吊销（revoked=false）。
	ek, err := s.GetEnrollKey("once")
	if err != nil {
		t.Fatal(err)
	}
	if !ek.Consumed {
		t.Fatal("消费后 key 应标记 Consumed=true")
	}
	if ek.Revoked {
		t.Fatal("消费不应改动 Revoked（吊销专属管理员语义）")
	}
	// 第二次消费返回 false
	ok2, err := s.ConsumeOneTimeKey("once")
	if err != nil {
		t.Fatal(err)
	}
	if ok2 {
		t.Fatal("第二次消费应返回 false")
	}
}

// TestConsumeOneTimeKey_Concurrent N 个 goroutine 并发消费同一 key，恰好 1 个返回 true。
// 验证原子 UPDATE ... WHERE revoked=false 消除 TOCTOU 重放（配合 -race 运行）。
func TestConsumeOneTimeKey_Concurrent(t *testing.T) {
	s := newMemStore(t)
	// :memory: SQLite 在多连接下各连接是独立库，限制为单连接以保证一致视图。
	sqlDB, err := s.DB().DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)

	if err := s.CreateEnrollKey(&EnrollKey{Key: "race-key"}); err != nil {
		t.Fatal(err)
	}

	const n = 32
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		winCount int
	)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ok, err := s.ConsumeOneTimeKey("race-key")
			if err != nil {
				t.Errorf("ConsumeOneTimeKey: %v", err)
				return
			}
			if ok {
				mu.Lock()
				winCount++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()

	if winCount != 1 {
		t.Fatalf("恰好应有 1 个并发消费成功，实际 %d", winCount)
	}
}

// TestUnconsumeOneTimeKey_ReusableUntouched 验证补偿原语只作用于一次性 key：
// reusable=true 的 key 不被 Unconsume 触碰（reusable=false 守卫）。
func TestUnconsumeOneTimeKey_ReusableUntouched(t *testing.T) {
	st := newMemStore(t)

	if err := st.CreateEnrollKey(&EnrollKey{
		Key:      "reusable-key",
		Reusable: true,
		Consumed: true, // 人为置位，验证补偿不会复位它
	}); err != nil {
		t.Fatalf("CreateEnrollKey: %v", err)
	}

	if err := st.UnconsumeOneTimeKey("reusable-key"); err != nil {
		t.Fatalf("UnconsumeOneTimeKey: %v", err)
	}

	ek, err := st.GetEnrollKey("reusable-key")
	if err != nil {
		t.Fatalf("GetEnrollKey: %v", err)
	}
	if !ek.Consumed {
		t.Fatal("补偿不应触碰 reusable key 的 Consumed 状态")
	}
}

// TestEnrollKeyHasConsumedColumn 验证 AutoMigrate 自动补出 consumed 列。
func TestEnrollKeyHasConsumedColumn(t *testing.T) {
	st := newMemStore(t)
	if !st.DB().Migrator().HasColumn(&EnrollKey{}, "consumed") {
		t.Fatal("AutoMigrate 应补出 enroll_keys.consumed 列")
	}
}

// ---------- ACLPolicy ----------

func TestGetLatestACLPolicyEmpty(t *testing.T) {
	s := newMemStore(t)
	p, err := s.GetLatestACLPolicy()
	if err != nil {
		t.Fatalf("want nil error on empty, got %v", err)
	}
	if p.Version != 0 {
		t.Fatalf("want Version=0, got %d", p.Version)
	}
}

func TestSaveAndGetLatestACLPolicy(t *testing.T) {
	s := newMemStore(t)
	_, err := s.SaveACLPolicy(`{"acls":[]}`, "admin")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := s.SaveACLPolicy(`{"acls":[{"action":"accept"}]}`, "admin2")
	if err != nil {
		t.Fatal(err)
	}
	latest, err := s.GetLatestACLPolicy()
	if err != nil {
		t.Fatal(err)
	}
	if latest.Version != p2.Version {
		t.Fatalf("latest version = %d, want %d", latest.Version, p2.Version)
	}
	if latest.Author != "admin2" {
		t.Fatalf("author = %q, want admin2", latest.Author)
	}
}

// ---------- CARoot ----------

func TestGetCARootNotFound(t *testing.T) {
	s := newMemStore(t)
	_, err := s.GetCARoot()
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestSaveAndGetCARoot(t *testing.T) {
	s := newMemStore(t)
	cert := []byte("cert-pem")
	enc := []byte("encrypted-key")
	if err := s.SaveCARoot(cert, enc); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetCARoot()
	if err != nil {
		t.Fatal(err)
	}
	if string(got.CertPEM) != "cert-pem" {
		t.Fatalf("certPEM = %q, want cert-pem", got.CertPEM)
	}
	if string(got.EncKeyPEM) != "encrypted-key" {
		t.Fatalf("encKeyPEM = %q, want encrypted-key", got.EncKeyPEM)
	}
}

func TestSaveCARootIdempotent(t *testing.T) {
	s := newMemStore(t)
	if err := s.SaveCARoot([]byte("v1-cert"), []byte("v1-enc")); err != nil {
		t.Fatal(err)
	}
	// 覆盖
	if err := s.SaveCARoot([]byte("v2-cert"), []byte("v2-enc")); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetCARoot()
	if err != nil {
		t.Fatal(err)
	}
	if string(got.CertPEM) != "v2-cert" {
		t.Fatalf("certPEM = %q, want v2-cert", got.CertPEM)
	}
}

// ---------- RelayInfo ----------

func TestUpsertAndListRelayInfo(t *testing.T) {
	s := newMemStore(t)
	info := &RelayInfo{
		NodeID:         "relay1",
		TunnelEndpoint: "relay1.example.com:443",
		UDPEndpoint:    "relay1.example.com:3478",
		Protocols:      "tcp,udp",
		Priority:       10,
	}
	if err := s.UpsertRelayInfo(info); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListRelayInfo()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].NodeID != "relay1" {
		t.Fatalf("unexpected relay info list: %v", list)
	}
	// Upsert 更新
	info.Priority = 20
	if err := s.UpsertRelayInfo(info); err != nil {
		t.Fatal(err)
	}
	list, _ = s.ListRelayInfo()
	if list[0].Priority != 20 {
		t.Fatalf("priority = %d, want 20", list[0].Priority)
	}
}

// ---------- RelayLinks ----------

func TestSetAndListRelayLinks(t *testing.T) {
	s := newMemStore(t)
	if err := s.SetRelayLinks("r1", []string{"r2", "r3"}); err != nil {
		t.Fatal(err)
	}
	links, err := s.ListRelayLinks()
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 2 {
		t.Fatalf("got %d links, want 2", len(links))
	}
}

func TestSetRelayLinksReplaces(t *testing.T) {
	s := newMemStore(t)
	if err := s.SetRelayLinks("r1", []string{"r2", "r3"}); err != nil {
		t.Fatal(err)
	}
	// 全量替换
	if err := s.SetRelayLinks("r1", []string{"r4"}); err != nil {
		t.Fatal(err)
	}
	links, err := s.ListRelayLinks()
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].NeighborID != "r4" {
		t.Fatalf("unexpected links after replace: %v", links)
	}
}

func TestSetRelayLinksClear(t *testing.T) {
	s := newMemStore(t)
	if err := s.SetRelayLinks("r1", []string{"r2"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRelayLinks("r1", nil); err != nil {
		t.Fatal(err)
	}
	links, _ := s.ListRelayLinks()
	if len(links) != 0 {
		t.Fatalf("expected 0 links after clear, got %d", len(links))
	}
}

// ---------- Node.User 字段 ----------

func TestNodeUserField(t *testing.T) {
	s := newMemStore(t)
	n := &Node{ID: "n1", Role: "node", WGPubKey: "pk1", VirtualIP: "100.64.0.2", User: "alice"}
	if err := s.CreateNode(n); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetNode("n1")
	if err != nil {
		t.Fatal(err)
	}
	if got.User != "alice" {
		t.Fatalf("User = %q, want alice", got.User)
	}
}

// ---------- NewTables in Migrate ----------

func TestMigrateCreatesNewTables(t *testing.T) {
	s := newMemStore(t)
	for _, tbl := range []string{"ca_roots", "relay_infos"} {
		if !s.DB().Migrator().HasTable(tbl) {
			t.Errorf("缺少表: %s", tbl)
		}
	}
}

// ---------- UnconsumeOneTimeKey（补偿原语，bug #17） ----------

// TestUnconsumeOneTimeKey 验证补偿原语：把本次抢占烧毁的一次性 key 复位为可用。
func TestUnconsumeOneTimeKey(t *testing.T) {
	st := newMemStore(t)

	exp := time.Now().Add(time.Hour)
	if err := st.CreateEnrollKey(&EnrollKey{
		Key:       "comp-key",
		Reusable:  false,
		ExpiresAt: &exp,
		Tag:       "user-x",
	}); err != nil {
		t.Fatalf("CreateEnrollKey: %v", err)
	}

	// 先原子抢占（模拟 enroll 第 2 步），key 进入 consumed=true 烧毁态。
	consumed, err := st.ConsumeOneTimeKey("comp-key")
	if err != nil {
		t.Fatalf("ConsumeOneTimeKey: %v", err)
	}
	if !consumed {
		t.Fatal("首次抢占应成功")
	}

	// 补偿复位：模拟后续步骤失败，key 不应被永久烧毁。
	if err := st.UnconsumeOneTimeKey("comp-key"); err != nil {
		t.Fatalf("UnconsumeOneTimeKey: %v", err)
	}

	ek, err := st.GetEnrollKey("comp-key")
	if err != nil {
		t.Fatalf("GetEnrollKey: %v", err)
	}
	if ek.Consumed {
		t.Error("补偿后 key 应复位为未消费（Consumed=false）")
	}

	// 复位后可再次抢占成功（证明 key 重新可用）。
	consumed2, err := st.ConsumeOneTimeKey("comp-key")
	if err != nil {
		t.Fatalf("二次 ConsumeOneTimeKey: %v", err)
	}
	if !consumed2 {
		t.Error("补偿后应能再次抢占成功")
	}
}

// TestUnconsumeOneTimeKey_RevokeDuringEnroll 是本次修复的核心正确性用例：
// enroll 抢占（Consume）成功后、补偿（Unconsume）之前，若管理员主动吊销该 key
// （RevokeEnrollKey → revoked=true），补偿必须只复位 consumed、绝不触碰 revoked，
// 否则会“复活”管理员吊销的 key（TOCTOU 缺陷）。
func TestUnconsumeOneTimeKey_RevokeDuringEnroll(t *testing.T) {
	st := newMemStore(t)

	if err := st.CreateEnrollKey(&EnrollKey{
		Key:      "revoke-during-enroll",
		Reusable: false,
	}); err != nil {
		t.Fatalf("CreateEnrollKey: %v", err)
	}

	// 1. enroll 抢占消费成功。
	consumed, err := st.ConsumeOneTimeKey("revoke-during-enroll")
	if err != nil {
		t.Fatalf("ConsumeOneTimeKey: %v", err)
	}
	if !consumed {
		t.Fatal("抢占应成功")
	}

	// 2. 管理员在 enroll 中途吊销该 key。
	if err := st.RevokeEnrollKey("revoke-during-enroll"); err != nil {
		t.Fatalf("RevokeEnrollKey: %v", err)
	}

	// 3. enroll 后续步骤失败触发补偿。
	if err := st.UnconsumeOneTimeKey("revoke-during-enroll"); err != nil {
		t.Fatalf("UnconsumeOneTimeKey: %v", err)
	}

	// 核心断言：吊销必须保持，补偿不得复活。
	ek, err := st.GetEnrollKey("revoke-during-enroll")
	if err != nil {
		t.Fatalf("GetEnrollKey: %v", err)
	}
	if !ek.Revoked {
		t.Fatal("补偿复活了管理员吊销的 key（Revoked 被错误复位）")
	}

	// 已吊销的 key 不应能再被抢占消费（双重保险）。
	again, err := st.ConsumeOneTimeKey("revoke-during-enroll")
	if err != nil {
		t.Fatalf("ConsumeOneTimeKey(再次): %v", err)
	}
	if again {
		t.Fatal("已吊销 key 不应能再被消费")
	}
}

// TestUnconsumeOneTimeKey_ManualRevokedUntouched 验证补偿原语对已被管理员吊销的
// key 调用安全：补偿只复位 consumed，绝不触碰 revoked（吊销保持）。
func TestUnconsumeOneTimeKey_ManualRevokedUntouched(t *testing.T) {
	st := newMemStore(t)

	if err := st.CreateEnrollKey(&EnrollKey{
		Key:      "manual-revoked",
		Reusable: false,
		Revoked:  true, // 人工吊销
	}); err != nil {
		t.Fatalf("CreateEnrollKey: %v", err)
	}

	// 补偿原语调用应安全（幂等），不报错。
	if err := st.UnconsumeOneTimeKey("manual-revoked"); err != nil {
		t.Fatalf("UnconsumeOneTimeKey: %v", err)
	}

	// 吊销保持。
	ek, err := st.GetEnrollKey("manual-revoked")
	if err != nil {
		t.Fatalf("GetEnrollKey: %v", err)
	}
	if !ek.Revoked {
		t.Fatal("补偿不应触碰管理员吊销位")
	}
}

// TestEnrollKey_MigrateBackfillsConsumed 验证升级路径：旧库（enroll_keys 无 consumed 列）
// AutoMigrate 加列时，存量行的 consumed 必须回填为 false（而非 NULL）。否则旧一次性 key
// 会因 ConsumeOneTimeKey 的 `consumed=false` 在 SQL 三值逻辑下不匹配 NULL 而永久无法入网。
func TestEnrollKey_MigrateBackfillsConsumed(t *testing.T) {
	s, err := Open("sqlite://:memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// 模拟旧库：手动建不含 consumed 列的 enroll_keys 表并插入一行存量一次性 key。
	if err := s.db.Exec("CREATE TABLE `enroll_keys` (`key` text PRIMARY KEY, `reusable` numeric, `expires_at` datetime, `tag` text, `revoked` numeric, `created_at` datetime)").Error; err != nil {
		t.Fatalf("建旧表: %v", err)
	}
	if err := s.db.Exec("INSERT INTO `enroll_keys` (`key`, `reusable`, `revoked`) VALUES ('legacy', 0, 0)").Error; err != nil {
		t.Fatalf("插存量行: %v", err)
	}
	// 升级：Migrate 用新 EnrollKey（Consumed not null default false）加列并回填存量行。
	if err := s.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	// 存量行 consumed 应回填 false（非 NULL）。
	var nullCount int64
	if err := s.db.Raw("SELECT count(*) FROM `enroll_keys` WHERE consumed IS NULL").Scan(&nullCount).Error; err != nil {
		t.Fatalf("查 NULL: %v", err)
	}
	if nullCount != 0 {
		t.Fatalf("升级后存量行 consumed 为 NULL（%d 行），旧一次性 key 将永久无法入网", nullCount)
	}
	// 旧一次性 key 仍可被消费（consumed=false 才能原子抢占）。
	ok, err := s.ConsumeOneTimeKey("legacy")
	if err != nil {
		t.Fatalf("ConsumeOneTimeKey: %v", err)
	}
	if !ok {
		t.Fatal("升级后旧一次性 key 应可消费（consumed 回填 false），实际被拒")
	}
}
