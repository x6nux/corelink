import { useState, useEffect, useCallback } from 'react'
import { listKeys, createKey, revokeKey, getCA, type EnrollKey, type CAInfo } from '../api'

function formatDate(s: string | undefined): string {
  if (!s) return '-'
  return new Date(s).toLocaleString('zh-CN', { timeZone: 'Asia/Shanghai' })
}

function buildJoinToken(addr: string, key: string, caHash: string): string {
  const obj = { v: 1, h: addr, k: key, c: caHash }
  const json = JSON.stringify(obj)
  return btoa(json).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

function buildNodeConfig(addr: string, key: string, caHash: string): string {
  return JSON.stringify({
    controller: addr,
    enrollment_key: key,
    controller_ca_hash: caHash,
  }, null, 2)
}

export default function KeysPage() {
  const [keys, setKeys] = useState<EnrollKey[]>([])
  const [ca, setCA] = useState<CAInfo | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [showCreate, setShowCreate] = useState(false)
  const [revoking, setRevoking] = useState<string | null>(null)
  const [configModal, setConfigModal] = useState<{ key: string; host: string } | null>(null)
  const [copied, setCopied] = useState('')

  const [newTag, setNewTag] = useState('')
  const [newTTL, setNewTTL] = useState('0')
  const [creating, setCreating] = useState(false)
  const [createError, setCreateError] = useState('')

  const load = useCallback(async () => {
    setLoading(true); setError('')
    try {
      const [k, c] = await Promise.all([listKeys(), getCA()])
      setKeys(k); setCA(c)
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : '加载失败')
    } finally { setLoading(false) }
  }, [])

  useEffect(() => { load() }, [load])

  async function handleCreate() {
    setCreateError(''); setCreating(true)
    try {
      const ttl = parseInt(newTTL, 10)
      if (isNaN(ttl) || ttl < 0) throw new Error('TTL 必须为非负整数（秒）')
      await createKey({ tag: newTag, ttl_seconds: ttl })
      setShowCreate(false); setNewTag(''); setNewTTL('0')
      await load()
    } catch (e: unknown) {
      setCreateError(e instanceof Error ? e.message : '创建失败')
    } finally { setCreating(false) }
  }

  async function handleRevoke(key: string) {
    if (!confirm(`确认吊销密钥 ${key.slice(0, 16)}...？`)) return
    setRevoking(key)
    try {
      await revokeKey(key)
      setKeys(prev => prev.map(k => k.key === key ? { ...k, revoked: true } : k))
    } catch (e: unknown) {
      alert(e instanceof Error ? e.message : '吊销失败')
    } finally { setRevoking(null) }
  }

  function openConfig(key: string) {
    // 从当前浏览器地址提取 controller 入口（host:port）
    const loc = window.location
    const defaultAddr = `${loc.hostname}:${loc.port || '7443'}`
    setConfigModal({ key, host: defaultAddr })
  }

  function copyText(text: string, label: string) {
    navigator.clipboard.writeText(text).then(() => {
      setCopied(label)
      setTimeout(() => setCopied(''), 2000)
    }).catch(() => {})
  }

  const caHash = ca?.ca_hash ?? 'sha256:...'

  return (
    <div>
      <div className="page-header">
        <div>
          <div className="page-title">注册密钥</div>
          <div className="page-subtitle">管理节点注册使用的 Enrollment Key</div>
        </div>
        <div className="flex-gap">
          <button className="btn-secondary" onClick={load} disabled={loading}>刷新</button>
          <button className="btn-primary" onClick={() => setShowCreate(true)}>生成密钥</button>
        </div>
      </div>

      {error && <div className="alert alert-error">{error}</div>}

      <div className="card" style={{ padding: 0 }}>
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>状态</th>
                <th>密钥</th>
                <th>标签</th>
                <th>过期时间</th>
                <th>创建时间</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              {loading ? (
                <tr><td colSpan={6} className="loading">加载中...</td></tr>
              ) : keys.length === 0 ? (
                <tr><td colSpan={6} className="loading">暂无密钥</td></tr>
              ) : keys.map(k => (
                <tr key={k.key} style={{ opacity: k.revoked ? 0.55 : 1 }}>
                  <td>
                    <span className={`badge ${k.revoked ? 'badge-danger' : 'badge-success'}`}>
                      {k.revoked ? '已吊销' : '有效'}
                    </span>
                  </td>
                  <td>
                    <span className="monospace truncate" title={k.key}>{k.key.slice(0, 20)}...</span>
                  </td>
                  <td>{k.tag || '-'}</td>
                  <td style={{ fontSize: 12 }}>{formatDate(k.expires_at)}</td>
                  <td style={{ fontSize: 12 }}>{formatDate(k.created_at)}</td>
                  <td>
                    <div className="flex-gap" style={{ gap: 4 }}>
                      <button className="btn-primary" style={{ fontSize: 12, padding: '4px 10px' }}
                        onClick={() => openConfig(k.key)} disabled={k.revoked}>
                        接入配置
                      </button>
                      <button className="btn-danger" style={{ fontSize: 12, padding: '4px 10px' }}
                        onClick={() => handleRevoke(k.key)} disabled={k.revoked || revoking === k.key}>
                        {revoking === k.key ? '吊销中...' : '吊销'}
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>

      {/* 接入配置弹窗 */}
      {configModal && (
        <div className="modal-overlay" onClick={() => setConfigModal(null)}>
          <div className="modal" style={{ maxWidth: 640 }} onClick={e => e.stopPropagation()}>
            <div className="modal-title">节点接入配置</div>

            <div className="form-group">
              <label>Controller 入口地址 (host:port)</label>
              <input type="text" value={configModal.host}
                onChange={e => setConfigModal({ ...configModal, host: e.target.value })}
                placeholder="controller.example.com:7443" />
              <div style={{ fontSize: 11, color: 'var(--color-text-muted)', marginTop: 4 }}>
                填写完整地址 host:port，修改后下方配置实时更新
              </div>
            </div>

            {/* 一键安装命令 */}
            <div className="form-group">
              <label>一键安装</label>
              <div style={{ position: 'relative' }}>
                <pre className="code-block">{`corelink-node install --token ${buildJoinToken(configModal.host, configModal.key, caHash)}`}</pre>
                <button className="btn-copy" onClick={() => copyText(
                  `corelink-node install --token ${buildJoinToken(configModal.host, configModal.key, caHash)}`,
                  'cmd'
                )}>{copied === 'cmd' ? '已复制' : '复制'}</button>
              </div>
            </div>

            {/* Join Token */}
            <div className="form-group">
              <label>Join Token</label>
              <div style={{ position: 'relative' }}>
                <pre className="code-block" style={{ wordBreak: 'break-all' }}>{buildJoinToken(configModal.host, configModal.key, caHash)}</pre>
                <button className="btn-copy" onClick={() => copyText(
                  buildJoinToken(configModal.host, configModal.key, caHash), 'token'
                )}>{copied === 'token' ? '已复制' : '复制'}</button>
              </div>
            </div>

            {/* 完整配置 JSON */}
            <div className="form-group">
              <label>完整配置文件 (/etc/corelink-node.json)</label>
              <div style={{ position: 'relative' }}>
                <pre className="code-block">{buildNodeConfig(configModal.host, configModal.key, caHash)}</pre>
                <button className="btn-copy" onClick={() => copyText(
                  buildNodeConfig(configModal.host, configModal.key, caHash), 'json'
                )}>{copied === 'json' ? '已复制' : '复制'}</button>
              </div>
            </div>

            <div className="form-actions">
              <button className="btn-secondary" onClick={() => setConfigModal(null)}>关闭</button>
            </div>
          </div>
        </div>
      )}

      {/* 创建密钥弹窗 */}
      {showCreate && (
        <div className="modal-overlay" onClick={() => setShowCreate(false)}>
          <div className="modal" onClick={e => e.stopPropagation()}>
            <div className="modal-title">生成注册密钥</div>
            {createError && <div className="alert alert-error">{createError}</div>}
            <div className="form-group">
              <label>标签（可选）</label>
              <input type="text" value={newTag} onChange={e => setNewTag(e.target.value)} placeholder="例如: production-node" />
            </div>
            <div className="form-group">
              <label>TTL（秒，0 = 永不过期）</label>
              <input type="number" value={newTTL} onChange={e => setNewTTL(e.target.value)} min="0" placeholder="0" />
            </div>
            <div style={{ fontSize: 12, color: 'var(--color-text-muted)', padding: '6px 0' }}>
              注册密钥为一次性使用，每个节点需独立生成新密钥
            </div>
            <div className="form-actions">
              <button className="btn-secondary" onClick={() => setShowCreate(false)}>取消</button>
              <button className="btn-primary" onClick={handleCreate} disabled={creating}>
                {creating ? '生成中...' : '生成'}
              </button>
            </div>
          </div>
        </div>
      )}

      <style>{`
        .code-block { background: var(--color-bg); border: 1px solid var(--color-border); border-radius: 4px; padding: 10px 14px; font-family: 'SF Mono', 'Fira Code', monospace; font-size: 12px; line-height: 1.6; color: var(--color-text); white-space: pre-wrap; word-break: break-all; margin: 0; max-height: 200px; overflow-y: auto; }
        .btn-copy { position: absolute; top: 6px; right: 6px; background: var(--color-surface2); color: var(--color-text-muted); border: 1px solid var(--color-border); border-radius: 4px; padding: 2px 10px; font-size: 11px; cursor: pointer; }
        .btn-copy:hover { background: var(--color-primary); color: white; border-color: var(--color-primary); }
      `}</style>
    </div>
  )
}
