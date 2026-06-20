import { useState, useEffect, useCallback, useMemo } from 'react'
import { ComposableMap, Geographies, Geography, Line, Marker, ZoomableGroup } from 'react-simple-maps'
import { getTopology, setNodeGeo, type Topology, type TopoNode } from '../api'

const geoUrl = 'https://cdn.jsdelivr.net/npm/world-atlas@2/countries-110m.json'

function rttColor(ms: number): string {
  if (ms <= 0) return '#64748b'
  if (ms < 10) return '#34d399'
  if (ms < 50) return '#22d3ee'
  if (ms < 100) return '#fbbf24'
  return '#f87171'
}

// 同坐标节点按小圆环排列，返回偏移后的 [lon, lat]
function spreadCluster(nodes: TopoNode[]): Map<string, [number, number]> {
  const groups = new Map<string, TopoNode[]>()
  for (const n of nodes) {
    if (n.lat === 0 && n.lon === 0) continue
    const key = `${n.lat.toFixed(4)},${n.lon.toFixed(4)}`
    const arr = groups.get(key) || []
    arr.push(n)
    groups.set(key, arr)
  }
  const result = new Map<string, [number, number]>()
  for (const [, group] of groups) {
    if (group.length === 1) {
      result.set(group[0].id, [group[0].lon, group[0].lat])
    } else {
      const r = 1.2 // 偏移半径（经纬度）
      for (let i = 0; i < group.length; i++) {
        const angle = (2 * Math.PI * i) / group.length - Math.PI / 2
        result.set(group[i].id, [
          group[i].lon + r * Math.cos(angle),
          group[i].lat + r * Math.sin(angle),
        ])
      }
    }
  }
  return result
}

// 根据节点坐标计算最佳 center + zoom
function fitBounds(nodes: TopoNode[]): { center: [number, number]; zoom: number } {
  const positioned = nodes.filter(n => n.lat !== 0 || n.lon !== 0)
  if (positioned.length === 0) return { center: [105, 25], zoom: 1 }

  const lats = positioned.map(n => n.lat)
  const lons = positioned.map(n => n.lon)
  const minLat = Math.min(...lats), maxLat = Math.max(...lats)
  const minLon = Math.min(...lons), maxLon = Math.max(...lons)

  const centerLon = (minLon + maxLon) / 2
  const centerLat = (minLat + maxLat) / 2

  const spanLon = Math.max(maxLon - minLon, 5) // 最小跨度 5°
  const spanLat = Math.max(maxLat - minLat, 5)

  // 视口 SVG 尺寸 960x500，投影 scale=160 约覆盖 360° 宽
  // 粗略：zoom = 视口覆盖度 / 节点跨度，加 padding
  const zoomLon = 280 / spanLon
  const zoomLat = 160 / spanLat
  const zoom = Math.min(zoomLon, zoomLat, 20) // 上限 20x
  return { center: [centerLon, centerLat], zoom: Math.max(zoom, 1) }
}

export default function TopologyPage() {
  const [topo, setTopo] = useState<Topology | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [selected, setSelected] = useState<TopoNode | null>(null)
  const [editing, setEditing] = useState<TopoNode | null>(null)
  const [editLat, setEditLat] = useState('')
  const [editLon, setEditLon] = useState('')
  const [editCity, setEditCity] = useState('')
  const [saving, setSaving] = useState(false)

  const load = useCallback(async () => {
    try {
      setTopo(await getTopology())
      setError('')
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : '加载失败')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    load()
    const t = setInterval(load, 10000)
    return () => clearInterval(t)
  }, [load])

  const nodePositions = useMemo(() => topo ? spreadCluster(topo.nodes) : new Map(), [topo])

  const { center, zoom: fitZoom } = useMemo(
    () => fitBounds(topo?.nodes ?? []),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [topo?.nodes.map(n => `${n.id}:${n.lat}:${n.lon}`).join(',')]
  )

  function openEdit(n: TopoNode) {
    setEditing(n)
    setEditLat(String(n.lat))
    setEditLon(String(n.lon))
    setEditCity(n.city)
  }

  async function handleSave() {
    if (!editing) return
    setSaving(true)
    try {
      await setNodeGeo(editing.id, parseFloat(editLat), parseFloat(editLon), editCity)
      await load()
      setEditing(null)
    } catch (e: unknown) {
      alert(e instanceof Error ? e.message : '保存失败')
    } finally {
      setSaving(false)
    }
  }

  const pos = (id: string): [number, number] | null => nodePositions.get(id) ?? null

  const onlineCount = topo?.nodes.filter(n => n.online).length ?? 0
  const lowAccCount = topo?.nodes.filter(n => n.accuracy === 'low').length ?? 0

  // 根据 zoom 级别调整元素大小（zoom 大时元素缩小，保持视觉一致）
  const scaleFactor = 1 / fitZoom

  return (
    <div>
      <div className="page-header">
        <div>
          <div className="page-title">拓扑</div>
          <div className="page-subtitle">
            {topo ? `节点 ${topo.nodes.length} · 在线 ${onlineCount} · 连接 ${topo.physical_edges.length} · 路由 ${topo.active_routes.length}` : '加载中...'}
          </div>
        </div>
        <div className="flex-gap">
          <button className="btn-secondary" onClick={load} disabled={loading}>{loading ? '加载中...' : '刷新'}</button>
        </div>
      </div>

      {error && <div className="alert alert-error">{error}</div>}

      <div style={{ display: 'flex', gap: 16, alignItems: 'flex-start' }}>
        {/* 地图 */}
        <div className="card" style={{ flex: 1, padding: 0, overflow: 'hidden', background: '#0a0c12' }}>
          <ComposableMap
            projectionConfig={{ scale: 160, center: [0, 0] }}
            width={960}
            height={500}
            style={{ width: '100%', height: 'auto', backgroundColor: '#0a0c12' }}
          >
            <ZoomableGroup center={center} zoom={fitZoom} minZoom={1} maxZoom={24}>
              <Geographies geography={geoUrl}>
                {({ geographies }: { geographies: any[] }) =>
                  geographies.map(geo => (
                    <Geography
                      key={geo.rsmKey}
                      geography={geo}
                      fill="#161924"
                      stroke="#252a3a"
                      strokeWidth={0.4 * scaleFactor}
                      style={{ default: { outline: 'none' }, hover: { outline: 'none' }, pressed: { outline: 'none' } }}
                    />
                  ))
                }
              </Geographies>

              {/* 物理连接 */}
              {topo?.physical_edges.map((e, i) => {
                const a = pos(e.src), b = pos(e.dst)
                if (!a || !b) return null
                return <Line key={`phy-${i}`} from={a} to={b} stroke="#3a4055" strokeWidth={0.6 * scaleFactor} strokeLinecap="round" opacity={0.4} />
              })}

              {/* 路由线（RTT 着色） */}
              {topo?.active_routes.map((r, i) => {
                const a = pos(r.src), b = pos(r.dst)
                if (!a || !b) return null
                if (r.next_hop && r.next_hop !== r.dst) {
                  const mid = pos(r.next_hop)
                  if (mid) {
                    return (
                      <g key={`route-${i}`}>
                        <Line from={a} to={mid} stroke={rttColor(r.rtt_ms)} strokeWidth={1.6 * scaleFactor} strokeLinecap="round" />
                        <Line from={mid} to={b} stroke={rttColor(r.rtt_ms)} strokeWidth={1.6 * scaleFactor} strokeLinecap="round" opacity={0.6} />
                      </g>
                    )
                  }
                }
                return <Line key={`route-${i}`} from={a} to={b} stroke={rttColor(r.rtt_ms)} strokeWidth={1.6 * scaleFactor} strokeLinecap="round" />
              })}

              {/* 节点标记 */}
              {topo?.nodes.map(n => {
                const coord = pos(n.id)
                if (!coord) return null
                const color = n.online ? '#4f8ef7' : '#475569'
                const r = scaleFactor
                return (
                  <Marker key={n.id} coordinates={coord} onClick={() => setSelected(n)}>
                    {n.accuracy === 'low' && <circle r={8 * r} fill="none" stroke="#fbbf24" strokeWidth={1 * r} opacity={0.6} />}
                    {n.online && <circle r={6 * r} fill={color} opacity={0.25} />}
                    <circle r={(n.online ? 3.5 : 3) * r} fill={color} stroke="#0a0c12" strokeWidth={1 * r} cursor="pointer" />
                    {selected?.id === n.id && <circle r={10 * r} fill="none" stroke={color} strokeWidth={1.5 * r} />}
                    {/* 节点名称标签 */}
                    <text
                      y={-6 * r}
                      textAnchor="middle"
                      fill="#94a3b8"
                      fontSize={10 * r}
                      style={{ pointerEvents: 'none', userSelect: 'none' }}
                    >{n.name}</text>
                  </Marker>
                )
              })}
            </ZoomableGroup>
          </ComposableMap>

          {/* 图例 + 操作提示 */}
          <div style={{ display: 'flex', gap: 16, padding: '8px 14px', fontSize: 11, color: 'var(--color-text-muted)', borderTop: '1px solid var(--color-border)', flexWrap: 'wrap', alignItems: 'center' }}>
            <span><span style={{ color: rttColor(5) }}>●</span> &lt;10ms</span>
            <span><span style={{ color: rttColor(30) }}>●</span> 10-50ms</span>
            <span><span style={{ color: rttColor(80) }}>●</span> 50-100ms</span>
            <span><span style={{ color: rttColor(150) }}>●</span> &gt;100ms</span>
            <span style={{ marginLeft: 'auto', opacity: 0.6 }}>滚轮缩放 · 拖拽平移</span>
            <span>低精度 {lowAccCount}</span>
          </div>
        </div>

        {/* 侧栏 */}
        <div style={{ width: 260, flexShrink: 0 }}>
          <div className="card" style={{ padding: '12px 14px' }}>
            <div className="card-title" style={{ marginBottom: 8, fontSize: 13 }}>节点列表</div>
            <div style={{ maxHeight: 380, overflowY: 'auto' }}>
              {topo?.nodes.map(n => (
                <div key={n.id} onClick={() => setSelected(n)} style={{
                  padding: '6px 8px', borderRadius: 4, cursor: 'pointer', marginBottom: 2,
                  background: selected?.id === n.id ? 'var(--color-surface2)' : 'transparent',
                  display: 'flex', alignItems: 'center', gap: 8,
                }}>
                  <span style={{
                    width: 7, height: 7, borderRadius: '50%', flexShrink: 0,
                    background: n.online ? '#34d399' : '#475569',
                    boxShadow: n.online ? '0 0 6px #34d399' : 'none',
                  }} />
                  <div style={{ flex: 1, minWidth: 0 }}>
                    <div style={{ fontSize: 12, fontWeight: 600, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{n.name}</div>
                    <div style={{ fontSize: 10, color: 'var(--color-text-muted)' }}>{n.city || '-'}</div>
                  </div>
                </div>
              ))}
            </div>
          </div>

          {selected && (
            <div className="card" style={{ padding: '12px 14px', marginTop: 12 }}>
              <div className="card-title" style={{ marginBottom: 8, fontSize: 13 }}>{selected.name}</div>
              <div style={{ fontSize: 12, lineHeight: 1.8 }}>
                <div><span style={{ color: 'var(--color-text-muted)' }}>VIP:</span> <span className="monospace">{selected.vip}</span></div>
                <div><span style={{ color: 'var(--color-text-muted)' }}>城市:</span> {selected.city || '-'}</div>
                <div><span style={{ color: 'var(--color-text-muted)' }}>坐标:</span> {selected.lat.toFixed(2)}, {selected.lon.toFixed(2)}</div>
                <div><span style={{ color: 'var(--color-text-muted)' }}>机房:</span> {selected.col_iata || '-'}</div>
                <div><span style={{ color: 'var(--color-text-muted)' }}>CF RTT:</span> {selected.cf_rtt_ms ? `${selected.cf_rtt_ms.toFixed(1)}ms` : '-'}</div>
                <div>
                  <span style={{ color: 'var(--color-text-muted)' }}>精度:</span>{' '}
                  <span style={{ color: selected.accuracy === 'high' ? 'var(--color-success)' : selected.accuracy === 'low' ? 'var(--color-warning)' : 'var(--color-text-muted)' }}>
                    {selected.accuracy === 'high' ? '高' : selected.accuracy === 'low' ? '低' : selected.accuracy === 'manual' ? '手动' : '未知'}
                  </span>
                </div>
              </div>
              <button className="btn-secondary" style={{ width: '100%', marginTop: 10, fontSize: 12 }} onClick={() => openEdit(selected)}>修正位置</button>
            </div>
          )}
        </div>
      </div>

      {/* 手动修正弹窗 */}
      {editing && (
        <div className="modal-overlay" onClick={() => setEditing(null)}>
          <div className="modal" onClick={e => e.stopPropagation()}>
            <div className="modal-title">修正 {editing.name} 位置</div>
            <div className="form-group">
              <label>纬度</label>
              <input type="number" step="0.0001" value={editLat} onChange={e => setEditLat(e.target.value)} />
            </div>
            <div className="form-group">
              <label>经度</label>
              <input type="number" step="0.0001" value={editLon} onChange={e => setEditLon(e.target.value)} />
            </div>
            <div className="form-group">
              <label>城市</label>
              <input type="text" value={editCity} onChange={e => setEditCity(e.target.value)} />
            </div>
            <div className="form-actions">
              <button className="btn-secondary" onClick={() => setEditing(null)}>取消</button>
              <button className="btn-primary" onClick={handleSave} disabled={saving}>{saving ? '保存中...' : '保存'}</button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
