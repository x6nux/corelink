import { useState, useEffect, useCallback } from 'react'
import { listRelays, setTopology, type Relay } from '../api'

export default function RelaysPage() {
  const [relays, setRelays] = useState<Relay[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [editTopo, setEditTopo] = useState<{ relay: Relay; neighbors: string } | null>(null)
  const [topoSaving, setTopoSaving] = useState(false)
  const [topoSuccess, setTopoSuccess] = useState('')

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      setRelays(await listRelays())
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : '加载失败')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { load() }, [load])

  async function handleSaveTopo() {
    if (!editTopo) return
    setTopoSaving(true)
    setTopoSuccess('')
    try {
      const neighbors = editTopo.neighbors
        .split(/[\n,]/)
        .map(s => s.trim())
        .filter(Boolean)
      await setTopology(editTopo.relay.node_id, neighbors)
      setTopoSuccess('拓扑已更新')
      await load()
      setTimeout(() => { setEditTopo(null); setTopoSuccess('') }, 1200)
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : '保存失败')
    } finally {
      setTopoSaving(false)
    }
  }

  // Build adjacency for topology display
  const allRelayIds = relays.map(r => r.node_id)
  const edges: [string, string][] = []
  const seen = new Set<string>()
  for (const r of relays) {
    for (const nb of r.neighbors) {
      const key = [r.node_id, nb].sort().join('--')
      if (!seen.has(key)) {
        edges.push([r.node_id, nb])
        seen.add(key)
      }
    }
  }

  return (
    <div>
      <div className="page-header">
        <div>
          <div className="page-title">Relay</div>
          <div className="page-subtitle">中继节点列表与拓扑</div>
        </div>
        <button className="btn-secondary" onClick={load} disabled={loading}>
          {loading ? '加载中...' : '刷新'}
        </button>
      </div>

      {error && <div className="alert alert-error">{error}</div>}

      <div className="card" style={{ padding: 0 }}>
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>状态</th>
                <th>节点 ID</th>
                <th>隧道端点</th>
                <th>UDP 端点</th>
                <th>协议</th>
                <th>优先级</th>
                <th>邻居数</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              {loading ? (
                <tr><td colSpan={8} className="loading">加载中...</td></tr>
              ) : relays.length === 0 ? (
                <tr><td colSpan={8} className="loading">暂无 Relay</td></tr>
              ) : relays.map(r => (
                <tr key={r.node_id}>
                  <td>
                    <span className={`badge ${r.online ? 'badge-success' : 'badge-muted'}`}>
                      <span className={`dot ${r.online ? 'dot-online' : 'dot-offline'}`} />
                      {r.online ? '在线' : '离线'}
                    </span>
                  </td>
                  <td className="monospace truncate" title={r.node_id}>{r.node_id}</td>
                  <td className="monospace">{r.tunnel_endpoint || '-'}</td>
                  <td className="monospace">{r.udp_endpoint || '-'}</td>
                  <td>{r.protocols || '-'}</td>
                  <td>{r.priority}</td>
                  <td>
                    <span className="badge badge-muted">{r.neighbors.length}</span>
                  </td>
                  <td>
                    <button
                      className="btn-secondary"
                      style={{ fontSize: 12, padding: '4px 10px' }}
                      onClick={() => setEditTopo({ relay: r, neighbors: r.neighbors.join('\n') })}
                    >
                      编辑拓扑
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>

      {/* Topology visualization */}
      {relays.length > 0 && (
        <div className="card">
          <div className="card-title">拓扑邻接图</div>
          {edges.length === 0 ? (
            <div style={{ fontSize: 13, color: 'var(--color-text-muted)' }}>
              暂无邻接关系（所有 relay 孤立）
            </div>
          ) : (
            <div>
              {edges.map(([a, b]) => (
                <div key={`${a}-${b}`} className="flex-gap" style={{ marginBottom: 8 }}>
                  <div className="topo-node monospace">{a.slice(0, 16)}...</div>
                  <div className="topo-arrow">↔</div>
                  <div className="topo-node monospace">{b.slice(0, 16)}...</div>
                </div>
              ))}
            </div>
          )}
          <div style={{ marginTop: 12 }}>
            <div style={{ fontSize: 12, color: 'var(--color-text-muted)', marginBottom: 8 }}>
              节点列表（{allRelayIds.length} 个 Relay）：
            </div>
            <div className="topo-list">
              {allRelayIds.map(id => (
                <div key={id} className="topo-node monospace">{id.slice(0, 20)}</div>
              ))}
            </div>
          </div>
        </div>
      )}

      {/* Edit topology modal */}
      {editTopo && (
        <div className="modal-overlay" onClick={() => setEditTopo(null)}>
          <div className="modal" onClick={e => e.stopPropagation()}>
            <div className="modal-title">编辑 Relay 拓扑邻接</div>
            <div style={{ fontSize: 12, color: 'var(--color-text-muted)', marginBottom: 12 }}>
              Relay ID: <span className="monospace">{editTopo.relay.node_id}</span>
            </div>
            <div className="form-group">
              <label>邻居节点 ID（每行一个 或 逗号分隔）</label>
              <textarea
                className="code-editor"
                rows={6}
                value={editTopo.neighbors}
                onChange={e => setEditTopo({ ...editTopo, neighbors: e.target.value })}
                placeholder="输入邻居节点 ID..."
              />
            </div>
            {topoSuccess && <div className="alert alert-success">{topoSuccess}</div>}
            <div className="form-actions">
              <button className="btn-secondary" onClick={() => setEditTopo(null)}>取消</button>
              <button className="btn-primary" onClick={handleSaveTopo} disabled={topoSaving}>
                {topoSaving ? '保存中...' : '保存'}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
