package admin

import (
	"io/fs"
	"net/http"
	"strings"

	"github.com/x6nux/corelink/web"
)

// spaFS 是 web.FS 中 dist/ 子树的视图，使根路径 "/" 对应 dist/index.html。
var spaFS, _ = fs.Sub(web.FS, "dist")

// spaHandler 服务 SPA 静态资源：
//   - 请求路径的文件在 dist/ 存在 → 直接返回（CSS/JS/图片等）。
//   - 其他路径（前端路由）→ fallback 返回 dist/index.html。
//
// 该 handler 注册在 mux 的 "/" 兜底位置，API 路由 /admin/api/* 先被精确匹配，
// 不会落到这里。
func spaHandler() http.Handler {
	fileServer := http.FileServer(http.FS(spaFS))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 去掉 /admin 前缀（合并端口后所有请求都带此前缀），再清理前导 /
		path := strings.TrimPrefix(r.URL.Path, "/admin")
		path = strings.TrimPrefix(path, "/")
		// 尝试在 embed FS 内打开该路径。
		f, err := spaFS.Open(path)
		if err == nil {
			// 确认是文件而非目录（目录让 FileServer 自行处理，但我们不想暴露列目录）。
			st, statErr := f.Stat()
			f.Close()
			if statErr == nil && !st.IsDir() {
				// 用去掉 /admin 前缀的路径让 FileServer 定位文件
				r2 := r.Clone(r.Context())
				r2.URL.Path = "/" + path
				fileServer.ServeHTTP(w, r2)
				return
			}
		}
		// Fallback：返回 index.html，供前端路由（React Router）接管。
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		fileServer.ServeHTTP(w, r2)
	})
}
