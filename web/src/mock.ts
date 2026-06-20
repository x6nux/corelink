import type { Node, ACLPolicy, EnrollKey, Relay, Cert, CAInfo, LoginResponse, Topology } from './api'

const now = new Date().toISOString()
const hour = (h: number) => new Date(Date.now() - h * 3600000).toISOString()
const day = (d: number) => new Date(Date.now() - d * 86400000).toISOString()

export const mockNodes: Node[] = [
  { id: '100', name: 'ctrl', remark: 'Controller + Node', role: 'controller', hostname: 'ctrl.corelink.io', user: 'root', virtual_ip: '100.64.0.2', generation: 42, online: true },
  { id: '101', name: 'n2-us-east', remark: 'US East Gateway', role: 'transit', hostname: 'us-east.corelink.io', user: 'root', virtual_ip: '100.64.0.3', generation: 41, online: true },
  { id: '102', name: 'n3-cn-bj', remark: 'Beijing Office', role: 'leaf', hostname: 'bj-office.corelink.io', user: 'root', virtual_ip: '100.64.0.4', generation: 40, online: true },
  { id: '103', name: 'n4-cn-sh', remark: 'Shanghai Office', role: 'leaf', hostname: 'sh-office.corelink.io', user: 'root', virtual_ip: '100.64.0.5', generation: 40, online: true },
  { id: '104', name: 'n5-eu-de', remark: 'Frankfurt Relay', role: 'transit', hostname: 'de-relay.corelink.io', user: 'root', virtual_ip: '100.64.0.6', generation: 39, online: true },
  { id: '105', name: 'n6-jp-tk', remark: 'Tokyo Node', role: 'leaf', hostname: 'jp-tokyo.corelink.io', user: 'root', virtual_ip: '100.64.0.7', generation: 38, online: false },
  { id: '106', name: 'n7-home', remark: 'Home Desktop', role: 'leaf', hostname: 'home-pc', user: 'user', virtual_ip: '100.64.0.8', generation: 37, online: true },
  { id: '107', name: 'n8-laptop', remark: 'MacBook Pro', role: 'leaf', hostname: 'macbook-pro', user: 'user', virtual_ip: '100.64.0.9', generation: 36, online: false },
]

export const mockACL: ACLPolicy = {
  version: 5,
  author: 'admin',
  document: JSON.stringify({
    acls: [
      { action: 'accept', src: ['*'], dst: ['*:*'] },
    ],
    groups: {
      'group:office': ['n3-cn-bj', 'n4-cn-sh'],
      'group:relay': ['ctrl', 'n2-us-east', 'n5-eu-de'],
    },
  }, null, 2),
}

export const mockACLHistory: ACLPolicy[] = [
  { ...mockACL },
  { version: 4, author: 'admin', document: '{"acls":[{"action":"accept","src":["group:office"],"dst":["*:*"]}]}' },
  { version: 3, author: 'admin', document: '{"acls":[{"action":"accept","src":["*"],"dst":["*:22"]}]}' },
]

export const mockKeys: EnrollKey[] = [
  { key: 'ek-abc123def456',  tag: 'office-nodes', revoked: false, expires_at: day(-7), created_at: day(3) },
  { key: 'ek-xyz789ghi012',  tag: 'home-pc', revoked: false, created_at: day(10) },
  { key: 'ek-old456revoked',  tag: 'deprecated', revoked: true, created_at: day(30) },
  { key: 'ek-temp999short',  tag: 'temp-access', revoked: false, expires_at: hour(-2), created_at: hour(6) },
]

export const mockRelays: Relay[] = [
  { node_id: '100', tunnel_endpoint: '23.146.4.10:7446', udp_endpoint: '23.146.4.10:7445', protocols: 'tls,ws', priority: 1, online: true, neighbors: ['101', '104'] },
  { node_id: '101', tunnel_endpoint: '23.149.108.116:7446', udp_endpoint: '23.149.108.116:7445', protocols: 'tls,ws', priority: 2, online: true, neighbors: ['100', '104'] },
  { node_id: '104', tunnel_endpoint: '185.220.101.5:7446', udp_endpoint: '185.220.101.5:7445', protocols: 'tls', priority: 3, online: true, neighbors: ['100', '101'] },
]

export const mockCerts: Cert[] = [
  { serial: 'a1b2c3d4e5f6', node_id: '100', not_after: day(-365), revoked: false, created_at: day(30) },
  { serial: 'f6e5d4c3b2a1', node_id: '101', not_after: day(-365), revoked: false, created_at: day(28) },
  { serial: '112233445566', node_id: '102', not_after: day(-365), revoked: false, created_at: day(25) },
  { serial: 'aabbccddeeff', node_id: '103', not_after: day(-365), revoked: false, created_at: day(25) },
  { serial: 'deadbeef0001', node_id: '104', not_after: day(-365), revoked: false, created_at: day(20) },
  { serial: 'deadbeef0002', node_id: '105', not_after: day(-365), revoked: true, revoked_at: day(2), created_at: day(15) },
  { serial: 'deadbeef0003', node_id: '106', not_after: day(-365), revoked: false, created_at: day(10) },
  { serial: 'deadbeef0004', node_id: '107', not_after: day(-365), revoked: false, created_at: day(5) },
]

export const mockCA: CAInfo = {
  ca_cert_pem: `-----BEGIN CERTIFICATE-----
MIIBkDCB+gIJAMockCertCA001MA0GCSqGSIb3DQEBCwUAMBExDzANBgNVBAMMBkNv
cmVMaW5rIFJvb3QgQ0EwHhcNMjYwMTAxMDAwMDAwWhcNMzYwMTAxMDAwMDAwWjAR
MQ8wDQYDVQQDDAZDb3JlTGluayBSb290IENBMA0GCSqGSIb3DQEBCwUAA0EA...
-----END CERTIFICATE-----`,
  ca_hash: 'sha256:a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2',
}

export const mockTopology: Topology = {
  nodes: [
    { id: '100', name: 'ctrl', vip: '100.64.0.2', online: true, lat: 22.3, lon: 113.9, city: 'Hong Kong', country: 'HK', accuracy: 'high', cf_rtt_ms: 2, col_iata: 'HKG' },
    { id: '101', name: 'n2-us-east', vip: '100.64.0.3', online: true, lat: 40.7, lon: -74.0, city: 'New York', country: 'US', accuracy: 'high', cf_rtt_ms: 8, col_iata: 'EWR' },
    { id: '102', name: 'n3-cn-bj', vip: '100.64.0.4', online: true, lat: 39.9, lon: 116.4, city: 'Beijing', country: 'CN', accuracy: 'high', cf_rtt_ms: 15, col_iata: 'PEK' },
    { id: '103', name: 'n4-cn-sh', vip: '100.64.0.5', online: true, lat: 31.2, lon: 121.5, city: 'Shanghai', country: 'CN', accuracy: 'low', cf_rtt_ms: 12, col_iata: 'PVG' },
    { id: '104', name: 'n5-eu-de', vip: '100.64.0.6', online: true, lat: 50.1, lon: 8.7, city: 'Frankfurt', country: 'DE', accuracy: 'high', cf_rtt_ms: 6, col_iata: 'FRA' },
    { id: '105', name: 'n6-jp-tk', vip: '100.64.0.7', online: false, lat: 35.7, lon: 139.7, city: 'Tokyo', country: 'JP', accuracy: 'high', cf_rtt_ms: 0, col_iata: 'NRT' },
  ],
  physical_edges: [
    { src: '100', dst: '101' }, { src: '100', dst: '102' }, { src: '100', dst: '104' },
    { src: '101', dst: '104' }, { src: '102', dst: '103' }, { src: '104', dst: '105' },
  ],
  active_routes: [
    { src: '100', dst: '101', next_hop: '101', rtt_ms: 180 },
    { src: '100', dst: '102', next_hop: '102', rtt_ms: 25 },
    { src: '102', dst: '103', next_hop: '103', rtt_ms: 1 },
    { src: '101', dst: '104', next_hop: '104', rtt_ms: 90 },
    { src: '100', dst: '104', next_hop: '101', rtt_ms: 190 },
  ],
}

const delay = (ms = 200) => new Promise(r => setTimeout(r, ms + Math.random() * 100))

export const mockAPI = {
  async login(_user: string, _password: string): Promise<LoginResponse> {
    await delay()
    return { token: 'mock-token-' + Date.now(), user: 'admin' }
  },
  async logout(): Promise<void> { await delay(50) },
  async listNodes(): Promise<Node[]> { await delay(); return [...mockNodes] },
  async getNode(id: string): Promise<Node> { await delay(); return mockNodes.find(n => n.id === id) ?? mockNodes[0] },
  async deleteNode(_id: string): Promise<void> { await delay() },
  async getACL(): Promise<ACLPolicy> { await delay(); return { ...mockACL } },
  async putACL(document: string): Promise<ACLPolicy> { await delay(); return { ...mockACL, document, version: mockACL.version + 1 } },
  async getACLHistory(): Promise<ACLPolicy[]> { await delay(); return [...mockACLHistory] },
  async previewACL(_document: string): Promise<string[]> { await delay(); return ['102', '103'] },
  async listKeys(): Promise<EnrollKey[]> { await delay(); return [...mockKeys] },
  async createKey(opts: { tag: string; ttl_seconds: number }): Promise<EnrollKey> {
    await delay()
    return { key: 'ek-new-' + Date.now(), tag: opts.tag, revoked: false, expires_at: new Date(Date.now() + opts.ttl_seconds * 1000).toISOString(), created_at: now }
  },
  async revokeKey(_key: string): Promise<void> { await delay() },
  async listRelays(): Promise<Relay[]> { await delay(); return [...mockRelays] },
  async setTopology(_relay_id: string, _neighbors: string[]): Promise<void> { await delay() },
  async listCerts(): Promise<Cert[]> { await delay(); return [...mockCerts] },
  async revokeCert(_serial: string): Promise<void> { await delay() },
  async getCA(): Promise<CAInfo> { await delay(); return { ...mockCA } },
  async getTopology(): Promise<Topology> { await delay(); return JSON.parse(JSON.stringify(mockTopology)) },
}
