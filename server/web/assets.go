package web

import (
	"embed"
	"html/template"
)

// Templates contains the server web templates embedded into the binary.
//
//go:embed templates/telegram_login.html
var Templates embed.FS

// TelegramLoginTemplate is parsed once at startup and reused for every request.
var TelegramLoginTemplate = template.Must(template.ParseFS(Templates, "templates/telegram_login.html"))
