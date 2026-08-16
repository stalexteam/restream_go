package settings

import "restream_go/internal/control"

// Persist — атомарний запис config.json через control.Persist.
func Persist(configPath string, config *control.Dict) error {
	return control.Persist(configPath, config)
}
