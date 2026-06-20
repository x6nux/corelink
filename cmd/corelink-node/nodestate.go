package main

import (
	"sync"
	"time"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
)

type nodeState struct {
	mu        sync.RWMutex
	r         genv1.NodeTopoRole
	ver       uint64
	updatedAt time.Time // 拓扑版本实际更新时间
}

// newNodeState 用初始角色与拓扑版本构造 nodeState。
func newNodeState(role genv1.NodeTopoRole, ver uint64) *nodeState {
	return &nodeState{r: role, ver: ver}
}

// snapshot 原子读取当前 (role, topoVer) 的一致快照。
func (s *nodeState) snapshot() (genv1.NodeTopoRole, uint64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.r, s.ver
}

// role 读取当前角色（供 RPC Role() 闭包使用）。
func (s *nodeState) role() genv1.NodeTopoRole {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.r
}

// topoVer 读取当前拓扑版本（供 RPC TopoVer() 闭包使用）。
func (s *nodeState) topoVer() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ver
}

// topoUpdatedAt 返回拓扑版本最近更新时间。
func (s *nodeState) topoUpdatedAt() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.updatedAt
}

// set 成对更新角色与拓扑版本（供 OnConfig 回调使用）。
func (s *nodeState) set(role genv1.NodeTopoRole, ver uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ver != s.ver {
		s.updatedAt = time.Now()
	}
	s.r = role
	s.ver = ver
}

type nodeConfigSnapshot struct {
	mu sync.RWMutex
	nc *genv1.NodeConfig
}

func newNodeConfigSnapshot(nc *genv1.NodeConfig) *nodeConfigSnapshot {
	return &nodeConfigSnapshot{nc: nc}
}

func (s *nodeConfigSnapshot) get() *genv1.NodeConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.nc
}

func (s *nodeConfigSnapshot) set(nc *genv1.NodeConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nc = nc
}
