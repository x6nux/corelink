// API client for CoreLink Admin Console
// Endpoints: /admin/api/* (same origin)
// Mock 模式: VITE_MOCK=true 时使用本地 mock 数据（前端开发用）

import { mockAPI } from './mock'

const MOCK = import.meta.env.VITE_MOCK === 'true'
const BASE = '/admin/api'

function getToken(): string | null {
  return localStorage.getItem('admin_token')
}

export function setToken(token: string): void {
  localStorage.setItem('admin_token', token)
}

export function clearToken(): void {
  localStorage.removeItem('admin_token')
}

export function isLoggedIn(): boolean {
  if (MOCK) return !!getToken()
  return !!getToken()
}

async function request<T>(
  method: string,
  path: string,
  body?: unknown,
): Promise<T> {
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
  }
  const token = getToken()
  if (token) {
    headers['Authorization'] = `Bearer ${token}`
  }
  const res = await fetch(`${BASE}${path}`, {
    method,
    headers,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })
  if (res.status === 401) {
    clearToken()
    window.location.href = '/login'
    throw new Error('未授权，请重新登录')
  }
  if (!res.ok) {
    let errMsg = `HTTP ${res.status}`
    try {
      const data = await res.json()
      if (data.error) errMsg = data.error
    } catch {
      // ignore
    }
    throw new Error(errMsg)
  }
  // 204 no content
  if (res.status === 204) return undefined as T
  return res.json()
}

// ─── Auth ─────────────────────────────────────────────────────────────────────

export interface LoginResponse {
  token: string
  user: string
}

export async function login(user: string, password: string): Promise<LoginResponse> {
  if (MOCK) { const r = await mockAPI.login(user, password); setToken(r.token); return r }
  const data = await request<LoginResponse>('POST', '/login', { user, password })
  setToken(data.token)
  return data
}

export async function logout(): Promise<void> {
  if (MOCK) { await mockAPI.logout(); clearToken(); return }
  try {
    await request('POST', '/logout')
  } finally {
    clearToken()
  }
}

// ─── Nodes ────────────────────────────────────────────────────────────────────

export interface Node {
  id: string
  name: string
  remark: string
  role: string
  hostname: string
  user: string
  virtual_ip: string
  generation: number
  online: boolean
}

export async function listNodes(): Promise<Node[]> {
  if (MOCK) return mockAPI.listNodes()
  const data = await request<{ nodes: Node[] }>('GET', '/nodes')
  return data.nodes ?? []
}

export async function getNode(id: string): Promise<Node> {
  if (MOCK) return mockAPI.getNode(id)
  return request<Node>('GET', `/nodes/${id}`)
}

export async function deleteNode(id: string): Promise<void> {
  if (MOCK) return mockAPI.deleteNode(id)
  await request('DELETE', `/nodes/${id}`)
}

export async function patchNode(id: string, opts: { name?: string; remark?: string }): Promise<Node> {
  if (MOCK) {
    const n = await mockAPI.getNode(id)
    return { ...n, name: opts.name ?? n.name, remark: opts.remark ?? n.remark }
  }
  return request<Node>('PATCH', `/nodes/${id}`, opts)
}

// ─── ACL ──────────────────────────────────────────────────────────────────────

export interface ACLPolicy {
  version: number
  document: string
  author: string
}

export async function getACL(): Promise<ACLPolicy> {
  if (MOCK) return mockAPI.getACL()
  return request<ACLPolicy>('GET', '/acl')
}

export async function putACL(document: string): Promise<ACLPolicy> {
  if (MOCK) return mockAPI.putACL(document)
  return request<ACLPolicy>('PUT', '/acl', document)
}

export async function getACLHistory(): Promise<ACLPolicy[]> {
  if (MOCK) return mockAPI.getACLHistory()
  const data = await request<{ history: ACLPolicy[] }>('GET', '/acl/history')
  return data.history ?? []
}

export async function previewACL(document: string): Promise<string[]> {
  if (MOCK) return mockAPI.previewACL(document)
  const data = await request<{ changed_nodes: string[] }>('POST', '/acl/preview', document)
  return data.changed_nodes ?? []
}

// ─── Enrollment Keys ──────────────────────────────────────────────────────────

export interface EnrollKey {
  key: string
  tag: string
  revoked: boolean
  expires_at?: string
  created_at: string
}

export async function listKeys(): Promise<EnrollKey[]> {
  if (MOCK) return mockAPI.listKeys()
  const data = await request<{ keys: EnrollKey[] }>('GET', '/keys')
  return data.keys ?? []
}

export async function createKey(opts: {
  tag: string
  ttl_seconds: number
}): Promise<EnrollKey> {
  if (MOCK) return mockAPI.createKey(opts)
  return request<EnrollKey>('POST', '/keys', opts)
}

export async function revokeKey(key: string): Promise<void> {
  if (MOCK) return mockAPI.revokeKey(key)
  await request('DELETE', `/keys/${key}`)
}

// ─── Relays ───────────────────────────────────────────────────────────────────

export interface Relay {
  node_id: string
  tunnel_endpoint: string
  udp_endpoint: string
  protocols: string
  priority: number
  online: boolean
  neighbors: string[]
}

export async function listRelays(): Promise<Relay[]> {
  if (MOCK) return mockAPI.listRelays()
  const data = await request<{ relays: Relay[] }>('GET', '/relays')
  return data.relays ?? []
}

export async function setTopology(relay_id: string, neighbors: string[]): Promise<void> {
  if (MOCK) return mockAPI.setTopology(relay_id, neighbors)
  await request('PUT', '/relays/topology', { relay_id, neighbors })
}

// ─── Certs ────────────────────────────────────────────────────────────────────

export interface Cert {
  serial: string
  node_id: string
  not_after: string
  revoked: boolean
  revoked_at?: string
  created_at: string
}

export async function listCerts(): Promise<Cert[]> {
  if (MOCK) return mockAPI.listCerts()
  const data = await request<{ certs: Cert[] }>('GET', '/certs')
  return data.certs ?? []
}

export async function revokeCert(serial: string): Promise<void> {
  if (MOCK) return mockAPI.revokeCert(serial)
  await request('POST', `/certs/${serial}/revoke`)
}

export interface CAInfo {
  ca_cert_pem: string
  ca_hash: string
}

export async function getCA(): Promise<CAInfo> {
  if (MOCK) return mockAPI.getCA()
  return request<CAInfo>('GET', '/ca')
}

// ─── Topology ─────────────────────────────────────────────────────────────────

export interface TopoNode {
  id: string
  name: string
  vip: string
  online: boolean
  lat: number
  lon: number
  city: string
  country: string
  accuracy: string
  cf_rtt_ms: number
  col_iata?: string
}

export interface TopoEdge { src: string; dst: string }
export interface TopoRoute { src: string; dst: string; next_hop: string; rtt_ms: number }

export interface Topology {
  nodes: TopoNode[]
  physical_edges: TopoEdge[]
  active_routes: TopoRoute[]
}

export async function getTopology(): Promise<Topology> {
  if (MOCK) return mockAPI.getTopology()
  return request<Topology>('GET', '/topology')
}

export async function setNodeGeo(id: string, lat: number, lon: number, city: string): Promise<void> {
  if (MOCK) return
  await request('PUT', `/nodes/${id}/geo`, { lat, lon, city })
}
