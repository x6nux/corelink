import { useState, useEffect, useCallback } from 'react'
import { listNodes, deleteNode, patchNode, type Node } from '../api'

export default function NodesPage() {
  const [nodes, setNodes] = useState<Node[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [deleting, setDeleting] = useState<string | null>(null)
  const [editing, setEditing] = useState<Node | null>(null)
  const [editName, setEditName] = useState('')
  const [editRemark, setEditRemark] = useState('')
  const [saving, setSaving] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      setNodes(await listNodes())
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : '加载失败')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { load() }, [load])

  async function handleDelete(n: Node) {
    const label = n.name || n.hostname || n.id
    if (!confirm(`确认删除节点 "${label}" (ID: ${n.id})？\n\n此操作将吊销其证书并回收 IP，不可撤销。`)) return
    const id = n.id
    setDeleting(id)
    try {
      await deleteNode(id)
      setNodes(prev => prev.filter(n => n.id !== id))
    } catch (e: unknown) {
      alert(e instanceof Error ? e.message : '删除失败')
    } finally {
      setDeleting(null)
    }
  }

  function openEdit(n: Node) {
    setEditing(n)
    setEditName(n.name || '')
    setEditRemark(n.remark || '')
  }

  async function handleSave() {
    if (!editing) return
    setSaving(true)
    try {
      const updated = await patchNode(editing.id, { name: editName, remark: editRemark })
      setNodes(prev => prev.map(n => n.id === updated.id ? updated : n))
      setEditing(null)
    } catch (e: unknown) {
      alert(e instanceof Error ? e.message : '保存失败')
    } finally {
      setSaving(false)
    }
  }

  return (
    <div>
      <div className="page-header">
        <div>
          <div className="page-title">节点</div>
          <div className="page-subtitle">网络中已注册的所有节点</div>
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
                <th>ID</th>
                <th>名称</th>
                <th>虚拟 IP</th>
                <th>角色</th>
                <th>备注</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              {loading ? (
                <tr><td colSpan={7} className="loading">加载中...</td></tr>
              ) : nodes.length === 0 ? (
                <tr><td colSpan={7} className="loading">暂无节点</td></tr>
              ) : nodes.map(n => (
                <tr key={n.id}>
                  <td>
                    <span className={`badge ${n.online ? 'badge-success' : 'badge-muted'}`}>
                      <span className={`dot ${n.online ? 'dot-online' : 'dot-offline'}`} />
                      {n.online ? '在线' : '离线'}
                    </span>
                  </td>
                  <td className="monospace">{n.id}</td>
                  <td style={{ fontWeight: 600 }}>{n.name || n.hostname || '-'}</td>
                  <td className="monospace">{n.virtual_ip || '-'}</td>
                  <td>
                    <span className={`badge ${n.role === 'relay' ? 'badge-warning' : 'badge-muted'}`}>
                      {n.role}
                    </span>
                  </td>
                  <td style={{ color: 'var(--color-text-muted)' }}>{n.remark || '-'}</td>
                  <td>
                    <div className="flex-gap" style={{ gap: 4 }}>
                      <button
                        className="btn-secondary"
                        style={{ fontSize: 12, padding: '4px 10px' }}
                        onClick={() => openEdit(n)}
                      >
                        编辑
                      </button>
                      <button
                        className="btn-danger"
                        style={{ fontSize: 12, padding: '4px 10px' }}
                        onClick={() => handleDelete(n)}
                        disabled={deleting === n.id}
                      >
                        {deleting === n.id ? '删除中...' : '删除'}
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>

      <div style={{ fontSize: 12, color: 'var(--color-text-muted)', marginTop: 8 }}>
        共 {nodes.length} 个节点，在线 {nodes.filter(n => n.online).length} 个
      </div>

      {editing && (
        <div className="modal-overlay" onClick={() => setEditing(null)}>
          <div className="modal" onClick={e => e.stopPropagation()}>
            <div className="modal-title">编辑节点 {editing.id}</div>
            <div className="form-group">
              <label>名称</label>
              <input type="text" value={editName} onChange={e => setEditName(e.target.value)} placeholder={editing.hostname || '未命名'} />
            </div>
            <div className="form-group">
              <label>备注</label>
              <input type="text" value={editRemark} onChange={e => setEditRemark(e.target.value)} placeholder="添加备注..." />
            </div>
            <div className="form-actions">
              <button className="btn-secondary" onClick={() => setEditing(null)}>取消</button>
              <button className="btn-primary" onClick={handleSave} disabled={saving}>
                {saving ? '保存中...' : '保存'}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
