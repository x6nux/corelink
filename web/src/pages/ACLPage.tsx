import { useState, useEffect, useCallback } from 'react'
import { getACL, putACL, previewACL, listNodes, type ACLPolicy, type Node } from '../api'

interface FirewallRule {
  action: 'accept' | 'deny'
  src: string[]
  dst: string[]
  ports: string
  proto: string
  comment: string
}

interface FirewallGroup {
  name: string
  members: string[]
}

interface FirewallPolicy {
  rules: FirewallRule[]
  groups: FirewallGroup[]
}

function parsePolicy(doc: string): FirewallPolicy {
  try {
    const obj = JSON.parse(doc)
    const rules: FirewallRule[] = (obj.acls ?? []).map((a: Record<string, unknown>) => {
      const dstParts = ((a.dst as string[]) ?? ['*:*']).map(d => d)
      const ports = dstParts.map(d => { const p = d.split(':'); return p[1] ?? '*' }).join(',')
      const hosts = dstParts.map(d => d.split(':')[0])
      return {
        action: (a.action as string) ?? 'accept',
        src: (a.src as string[]) ?? ['*'],
        dst: hosts,
        ports: ports === '*' ? '' : ports,
        proto: (a.proto as string) ?? '',
        comment: (a.comment as string) ?? '',
      }
    })
    const groups: FirewallGroup[] = Object.entries(obj.groups ?? {}).map(([name, members]) => ({
      name,
      members: members as string[],
    }))
    return { rules, groups }
  } catch {
    return { rules: [], groups: [] }
  }
}

function serializePolicy(policy: FirewallPolicy): string {
  const acls = policy.rules.map(r => {
    const dst = r.dst.map(d => {
      const port = r.ports.trim() || '*'
      return `${d}:${port}`
    })
    const entry: Record<string, unknown> = { action: r.action, src: r.src, dst }
    if (r.proto) entry.proto = r.proto
    if (r.comment) entry.comment = r.comment
    return entry
  })
  const groups: Record<string, string[]> = {}
  policy.groups.forEach(g => { groups[g.name] = g.members })
  return JSON.stringify({ acls, groups }, null, 2)
}

const emptyRule = (): FirewallRule => ({
  action: 'accept', src: ['*'], dst: ['*'], ports: '', proto: '', comment: '',
})

export default function ACLPage() {
  const [current, setCurrent] = useState<ACLPolicy | null>(null)
  const [policy, setPolicy] = useState<FirewallPolicy>({ rules: [], groups: [] })
  const [nodes, setNodes] = useState<Node[]>([])
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [success, setSuccess] = useState('')
  const [previewResult, setPreviewResult] = useState<string[] | null>(null)
  const [editingGroup, setEditingGroup] = useState<number | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const [p, n] = await Promise.all([getACL(), listNodes()])
      setCurrent(p)
      setPolicy(parsePolicy(p.document ?? ''))
      setNodes(n)
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : '加载失败')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { load() }, [load])

  function updateRule(idx: number, patch: Partial<FirewallRule>) {
    setPolicy(prev => {
      const rules = [...prev.rules]
      rules[idx] = { ...rules[idx], ...patch }
      return { ...prev, rules }
    })
    setSuccess('')
    setPreviewResult(null)
  }

  function removeRule(idx: number) {
    setPolicy(prev => ({ ...prev, rules: prev.rules.filter((_, i) => i !== idx) }))
    setSuccess('')
  }

  function addRule() {
    setPolicy(prev => ({ ...prev, rules: [...prev.rules, emptyRule()] }))
  }

  function moveRule(idx: number, dir: -1 | 1) {
    setPolicy(prev => {
      const rules = [...prev.rules]
      const target = idx + dir
      if (target < 0 || target >= rules.length) return prev
      ;[rules[idx], rules[target]] = [rules[target], rules[idx]]
      return { ...prev, rules }
    })
  }

  async function handleSave() {
    setError(''); setSuccess(''); setSaving(true)
    try {
      const doc = serializePolicy(policy)
      const saved = await putACL(doc)
      setCurrent(saved)
      setSuccess(`保存成功 v${saved.version}`)
      setPreviewResult(null)
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : '保存失败')
    } finally { setSaving(false) }
  }

  async function handlePreview() {
    setError(''); setPreviewResult(null)
    try {
      const changed = await previewACL(serializePolicy(policy))
      setPreviewResult(changed)
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : '预览失败')
    }
  }

  // 节点名+分组名列表（用于下拉选择）
  const allTargets = [
    '*',
    ...nodes.map(n => n.name || n.id),
    ...policy.groups.map(g => g.name),
  ]

  function TagInput({ value, onChange, options }: { value: string[], onChange: (v: string[]) => void, options: string[] }) {
    const [input, setInput] = useState('')
    const filtered = options.filter(o => !value.includes(o) && o.toLowerCase().includes(input.toLowerCase()))
    return (
      <div className="tag-input">
        <div className="tags">
          {value.map(v => (
            <span key={v} className="tag">
              {v}
              <button onClick={() => onChange(value.filter(x => x !== v))}>x</button>
            </span>
          ))}
        </div>
        <div style={{ position: 'relative' }}>
          <input
            value={input} onChange={e => setInput(e.target.value)}
            placeholder="添加..." style={{ width: 100 }}
          />
          {input && filtered.length > 0 && (
            <div className="dropdown">
              {filtered.slice(0, 8).map(o => (
                <div key={o} className="dropdown-item" onClick={() => { onChange([...value, o]); setInput('') }}>{o}</div>
              ))}
            </div>
          )}
        </div>
      </div>
    )
  }

  return (
    <div>
      <div className="page-header">
        <div>
          <div className="page-title">防火墙</div>
          <div className="page-subtitle">
            {current ? `v${current.version} | 规则 ${policy.rules.length} 条 | 分组 ${policy.groups.length} 个` : '加载中...'}
          </div>
        </div>
        <div className="flex-gap">
          <button className="btn-secondary" onClick={handlePreview}>预览影响</button>
          <button className="btn-secondary" onClick={load} disabled={loading}>刷新</button>
          <button className="btn-primary" onClick={handleSave} disabled={saving}>
            {saving ? '保存中...' : '保存并下发'}
          </button>
        </div>
      </div>

      {error && <div className="alert alert-error">{error}</div>}
      {success && <div className="alert alert-success">{success}</div>}
      {previewResult !== null && (
        <div className="alert alert-info">
          {previewResult.length === 0 ? '此次变更不影响任何节点' : `影响 ${previewResult.length} 个节点: ${previewResult.join(', ')}`}
        </div>
      )}

      {loading ? <div className="loading">加载中...</div> : (
        <>
          {/* 规则列表 */}
          <div className="card">
            <div className="card-title" style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
              <span>规则（自上而下匹配，首条命中生效）</span>
              <button className="btn-primary" style={{ fontSize: 12, padding: '4px 12px' }} onClick={addRule}>+ 添加规则</button>
            </div>
            {policy.rules.length === 0 ? (
              <div style={{ padding: 20, textAlign: 'center', color: 'var(--color-text-muted)' }}>暂无规则（默认放行所有流量）</div>
            ) : (
              <table className="table">
                <thead>
                  <tr>
                    <th style={{ width: 40 }}>#</th>
                    <th style={{ width: 80 }}>动作</th>
                    <th>来源</th>
                    <th>目标</th>
                    <th style={{ width: 120 }}>端口</th>
                    <th style={{ width: 80 }}>协议</th>
                    <th>备注</th>
                    <th style={{ width: 100 }}>操作</th>
                  </tr>
                </thead>
                <tbody>
                  {policy.rules.map((rule, idx) => (
                    <tr key={idx}>
                      <td style={{ color: 'var(--color-text-muted)' }}>{idx + 1}</td>
                      <td>
                        <select value={rule.action} onChange={e => updateRule(idx, { action: e.target.value as 'accept' | 'deny' })}
                          style={{ background: 'var(--color-surface)', color: rule.action === 'accept' ? 'var(--color-success)' : 'var(--color-danger)', border: '1px solid var(--color-border)', borderRadius: 4, padding: '2px 4px', fontSize: 12 }}>
                          <option value="accept">允许</option>
                          <option value="deny">拒绝</option>
                        </select>
                      </td>
                      <td><TagInput value={rule.src} onChange={src => updateRule(idx, { src })} options={allTargets} /></td>
                      <td><TagInput value={rule.dst} onChange={dst => updateRule(idx, { dst })} options={allTargets} /></td>
                      <td>
                        <input value={rule.ports} onChange={e => updateRule(idx, { ports: e.target.value })}
                          placeholder="*" style={{ width: '100%', background: 'var(--color-surface)', color: 'var(--color-text)', border: '1px solid var(--color-border)', borderRadius: 4, padding: '2px 6px', fontSize: 12 }} />
                      </td>
                      <td>
                        <select value={rule.proto} onChange={e => updateRule(idx, { proto: e.target.value })}
                          style={{ background: 'var(--color-surface)', color: 'var(--color-text)', border: '1px solid var(--color-border)', borderRadius: 4, padding: '2px 4px', fontSize: 12 }}>
                          <option value="">全部</option>
                          <option value="tcp">TCP</option>
                          <option value="udp">UDP</option>
                          <option value="icmp">ICMP</option>
                        </select>
                      </td>
                      <td>
                        <input value={rule.comment} onChange={e => updateRule(idx, { comment: e.target.value })}
                          placeholder="-" style={{ width: '100%', background: 'var(--color-surface)', color: 'var(--color-text)', border: '1px solid var(--color-border)', borderRadius: 4, padding: '2px 6px', fontSize: 12 }} />
                      </td>
                      <td>
                        <div className="flex-gap" style={{ gap: 4 }}>
                          <button className="btn-icon" onClick={() => moveRule(idx, -1)} disabled={idx === 0} title="上移">^</button>
                          <button className="btn-icon" onClick={() => moveRule(idx, 1)} disabled={idx === policy.rules.length - 1} title="下移">v</button>
                          <button className="btn-icon btn-danger-icon" onClick={() => removeRule(idx)} title="删除">x</button>
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </div>

          {/* 节点分组 */}
          <div className="card" style={{ marginTop: 16 }}>
            <div className="card-title" style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
              <span>节点分组</span>
              <button className="btn-primary" style={{ fontSize: 12, padding: '4px 12px' }} onClick={() => {
                setPolicy(prev => ({ ...prev, groups: [...prev.groups, { name: 'group:new', members: [] }] }))
                setEditingGroup(policy.groups.length)
              }}>+ 添加分组</button>
            </div>
            {policy.groups.length === 0 ? (
              <div style={{ padding: 20, textAlign: 'center', color: 'var(--color-text-muted)' }}>暂无分组</div>
            ) : (
              <div style={{ display: 'flex', flexWrap: 'wrap', gap: 12 }}>
                {policy.groups.map((group, idx) => (
                  <div key={idx} className="card" style={{ padding: '10px 14px', minWidth: 200, flex: '0 0 auto' }}>
                    {editingGroup === idx ? (
                      <div>
                        <input value={group.name} onChange={e => {
                          const groups = [...policy.groups]; groups[idx] = { ...groups[idx], name: e.target.value }
                          setPolicy(prev => ({ ...prev, groups }))
                        }} style={{ width: '100%', marginBottom: 8, background: 'var(--color-surface)', color: 'var(--color-text)', border: '1px solid var(--color-border)', borderRadius: 4, padding: '4px 8px' }} />
                        <TagInput value={group.members} onChange={members => {
                          const groups = [...policy.groups]; groups[idx] = { ...groups[idx], members }
                          setPolicy(prev => ({ ...prev, groups }))
                        }} options={nodes.map(n => n.name || n.id)} />
                        <div className="flex-gap" style={{ marginTop: 8, gap: 4 }}>
                          <button className="btn-secondary" style={{ fontSize: 11, padding: '2px 8px' }} onClick={() => setEditingGroup(null)}>完成</button>
                          <button className="btn-secondary" style={{ fontSize: 11, padding: '2px 8px', color: 'var(--color-danger)' }} onClick={() => {
                            setPolicy(prev => ({ ...prev, groups: prev.groups.filter((_, i) => i !== idx) }))
                            setEditingGroup(null)
                          }}>删除</button>
                        </div>
                      </div>
                    ) : (
                      <div onClick={() => setEditingGroup(idx)} style={{ cursor: 'pointer' }}>
                        <div style={{ fontWeight: 600, fontSize: 13, marginBottom: 4 }}>{group.name}</div>
                        <div style={{ fontSize: 12, color: 'var(--color-text-muted)' }}>
                          {group.members.length === 0 ? '(空)' : group.members.join(', ')}
                        </div>
                      </div>
                    )}
                  </div>
                ))}
              </div>
            )}
          </div>
        </>
      )}

      <style>{`
        .tag-input { display: flex; flex-wrap: wrap; gap: 4px; align-items: center; }
        .tags { display: flex; flex-wrap: wrap; gap: 3px; }
        .tag { display: inline-flex; align-items: center; gap: 3px; background: var(--color-surface2); color: var(--color-primary); padding: 1px 6px; border-radius: 3px; font-size: 11px; }
        .tag button { background: none; border: none; color: var(--color-text-muted); cursor: pointer; font-size: 10px; padding: 0 2px; line-height: 1; }
        .tag button:hover { color: var(--color-danger); }
        .dropdown { position: absolute; top: 100%; left: 0; background: var(--color-surface); border: 1px solid var(--color-border); border-radius: 4px; z-index: 10; min-width: 140px; max-height: 200px; overflow-y: auto; box-shadow: 0 4px 12px rgba(0,0,0,.4); }
        .dropdown-item { padding: 4px 10px; cursor: pointer; font-size: 12px; }
        .dropdown-item:hover { background: var(--color-surface2); }
        .btn-icon { background: var(--color-surface2); color: var(--color-text-muted); border: 1px solid var(--color-border); border-radius: 3px; width: 22px; height: 22px; display: inline-flex; align-items: center; justify-content: center; font-size: 11px; cursor: pointer; padding: 0; }
        .btn-icon:hover { background: var(--color-border); color: var(--color-text); }
        .btn-icon:disabled { opacity: 0.3; cursor: default; }
        .btn-danger-icon:hover { background: var(--color-danger); color: white; }
      `}</style>
    </div>
  )
}
