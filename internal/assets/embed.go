// Package assets — шаблони первинного сетапу, вшиті в бінар.
package assets

import _ "embed"

// ConfigExample — config.example.json із плейсхолдерами __*__.
//
//go:embed config.example.json
var ConfigExample string

// OBSDockTemplate — обгортка дашборда для OBS Custom Browser Dock.
//
//go:embed obs-dock.html.template
var OBSDockTemplate string

// OBSSourceTemplate — трекер Start/Stop Streaming для OBS Browser Source.
//
//go:embed obs-source.html.template
var OBSSourceTemplate string
