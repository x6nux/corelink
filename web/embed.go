// Package web 导出嵌入的 React SPA 静态资源（web/dist）。
// 供 internal/controller/admin 包 import 后挂载到管理 HTTP server。
package web

import "embed"

// FS 包含构建产出的 SPA 静态资源（dist/）。
//
//go:embed all:dist
var FS embed.FS
