package web

import "embed"

// FS 内嵌前端静态资源(HTML/CSS/JS)。
//
//go:embed static
var FS embed.FS
