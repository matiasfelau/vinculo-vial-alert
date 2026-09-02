package postgres

import "gorm.io/gorm"

// AutoMigrate crea o actualiza el esquema de las tablas alerts y
// alert_destinations. AlertModel se migra primero: AlertDestinationModel
// depende de su FK.
func AutoMigrate(db *gorm.DB) error {
	return db.AutoMigrate(&AlertModel{}, &AlertDestinationModel{})
}
