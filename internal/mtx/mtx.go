// Package mtx — MediaMTX-обвʼязка контролера: рендер yml, старт/рестарт
// процесу, лог-watch, ping до площадок (mediamtx_*.py, ).
package mtx

import _ "embed"

// TemplateText — вшитий у бінар mediamtx.yml.template.
//
//go:embed mediamtx.yml.template
var TemplateText string
