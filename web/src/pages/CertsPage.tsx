import { useState, useEffect, useCallback } from 'react'
import { listCerts, revokeCert, getCA, type Cert, type CAInfo } from '../api'

function formatDate(s: string | undefined): string {
  if (!s) return '-'
  return new Date(s).toLocaleString('zh-CN', { timeZone: 'Asia/Shanghai' })
}

export default function CertsPage() {
  const [certs, setCerts] = useState<Cert[]>([])
  const [caInfo, setCAInfo] = useState<CAInfo | null>(null)
  const [loading, setLoading] = useState(true)
  const [caLoading, setCALoading] = useState(true)
  const [error, setError] = useState('')
  const [revoking, setRevoking] = useState<string | null>(null)
  const [copied, setCopied] = useState<string | null>(null)

  const loadCerts = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      setCerts(await listCerts())
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : '加载证书失败')
    } finally {
      setLoading(false)
    }
  }, [])

  const loadCA = useCallback(async () => {
    setCALoading(true)
    try {
      setCAInfo(await getCA())
    } catch {
      // CA 信息不一定可用，静默失败
    } finally {
      setCALoading(false)
    }
  }, [])

  useEffect(() => {
    loadCerts()
    loadCA()
  }, [loadCerts, loadCA])

  async function handleRevoke(serial: string) {
    if (!confirm(`确认吊销证书 ${serial}？`)) return
    setRevoking(serial)
    try {
      await revokeCert(serial)
      setCerts(prev => prev.map(c => c.serial === serial ? { ...c, revoked: true } : c))
    } catch (e: unknown) {
      alert(e instanceof Error ? e.message : '吊销失败')
    } finally {
      setRevoking(null)
    }
  }

  function copy(text: string, id: string) {
    navigator.clipboard.writeText(text).then(() => {
      setCopied(id)
      setTimeout(() => setCopied(null), 1500)
    })
  }

  const activeCerts = certs.filter(c => !c.revoked)
  const revokedCerts = certs.filter(c => c.revoked)

  return (
    <div>
      <div className="page-header">
        <div>
          <div className="page-title">证书 / CA</div>
          <div className="page-subtitle">已签发证书、CA 信息与服务端指纹</div>
        </div>
        <button className="btn-secondary" onClick={loadCerts} disabled={loading}>
          {loading ? '加载中...' : '刷新'}
        </button>
      </div>

      {error && <div className="alert alert-error">{error}</div>}

      {/* CA Info */}
      <div className="card">
        <div className="card-title">CA 信息</div>
        {caLoading ? (
          <div className="loading">加载中...</div>
        ) : caInfo ? (
          <>
            <div className="form-group">
              <label>服务端证书指纹（SHA-256）</label>
              <div style={{ position: 'relative' }}>
                <div className="code-block">{caInfo.ca_hash}</div>
                <button
                  className="copy-btn"
                  onClick={() => copy(caInfo.ca_hash, 'fp')}
                >
                  {copied === 'fp' ? '已复制!' : '复制'}
                </button>
              </div>
              <div style={{ fontSize: 12, color: 'var(--color-text-muted)', marginTop: 4 }}>
                配置 agent 时，将此指纹填入 ca_hash 字段
              </div>
            </div>
            <div className="form-group">
              <label>CA 证书 PEM</label>
              <div style={{ position: 'relative' }}>
                <div className="code-block" style={{ maxHeight: 200, overflow: 'auto' }}>
                  {caInfo.ca_cert_pem}
                </div>
                <button
                  className="copy-btn"
                  onClick={() => copy(caInfo.ca_cert_pem, 'pem')}
                >
                  {copied === 'pem' ? '已复制!' : '复制'}
                </button>
              </div>
            </div>
          </>
        ) : (
          <div className="alert alert-info">CA 信息不可用</div>
        )}
      </div>

      {/* Certs table */}
      <div className="card" style={{ padding: 0 }}>
        <div style={{ padding: '12px 16px', borderBottom: '1px solid var(--color-border)' }}>
          <div className="flex-gap">
            <span className="card-title" style={{ marginBottom: 0 }}>已签发证书</span>
            <span className="badge badge-success">有效 {activeCerts.length}</span>
            <span className="badge badge-danger">已吊销 {revokedCerts.length}</span>
          </div>
        </div>
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>状态</th>
                <th>序列号</th>
                <th>节点 ID</th>
                <th>到期时间</th>
                <th>创建时间</th>
                <th>吊销时间</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              {loading ? (
                <tr><td colSpan={7} className="loading">加载中...</td></tr>
              ) : certs.length === 0 ? (
                <tr><td colSpan={7} className="loading">暂无证书</td></tr>
              ) : certs.map(c => (
                <tr key={c.serial} style={{ opacity: c.revoked ? 0.55 : 1 }}>
                  <td>
                    <span className={`badge ${c.revoked ? 'badge-danger' : 'badge-success'}`}>
                      {c.revoked ? '已吊销' : '有效'}
                    </span>
                  </td>
                  <td className="monospace" style={{ fontSize: 12 }}>{c.serial}</td>
                  <td className="monospace truncate" title={c.node_id}>{c.node_id}</td>
                  <td style={{ fontSize: 12 }}>{formatDate(c.not_after)}</td>
                  <td style={{ fontSize: 12 }}>{formatDate(c.created_at)}</td>
                  <td style={{ fontSize: 12 }}>{c.revoked ? formatDate(c.revoked_at) : '-'}</td>
                  <td>
                    <button
                      className="btn-danger"
                      style={{ fontSize: 12, padding: '4px 10px' }}
                      onClick={() => handleRevoke(c.serial)}
                      disabled={c.revoked || revoking === c.serial}
                    >
                      {revoking === c.serial ? '吊销中...' : '吊销'}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  )
}
