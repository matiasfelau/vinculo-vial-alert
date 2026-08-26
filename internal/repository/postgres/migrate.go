package postgres

import "gorm.io/gorm"

// AutoMigrate crea o actualiza el esquema de la tabla alert_history.
func AutoMigrate(db *gorm.DB) error {
	return db.AutoMigrate(&AlertHistoryModel{})
}
