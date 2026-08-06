package model

import (
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

var DB *gorm.DB

func InitDatabase() {
	database, err := gorm.Open(postgres.Open(databaseDSN()), &gorm.Config{})
	if err != nil {
		log.Fatalf("cannot connect to PostgreSQL: %v", err)
	}

	sqlDB, err := database.DB()
	if err != nil {
		log.Fatalf("cannot configure PostgreSQL connection pool: %v", err)
	}

	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetMaxOpenConns(100)
	sqlDB.SetConnMaxLifetime(time.Hour)

	if err := database.AutoMigrate(&User{}, &AccessToken{}, &Link{}, &PollingSettings{}, &Comment{}, &Resource{}); err != nil {
		log.Fatalf("cannot run database migrations: %v", err)
	}

	DB = database
}

func EnsurePollingSettings() (PollingSettings, bool) {
	settings := NewPollingSettings()
	var existing PollingSettings
	if err := DB.First(&existing, 1).Error; err == nil {
		original := existing
		existing.Normalize()
		if existing.CommentPollIntervalSeconds != original.CommentPollIntervalSeconds || existing.MetricsPollIntervalSeconds != original.MetricsPollIntervalSeconds {
			if err := DB.Save(&existing).Error; err != nil {
				log.Fatalf("cannot normalize polling settings: %v", err)
			}
		}
		return existing, false
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		log.Fatalf("cannot load polling settings: %v", err)
	}

	if err := DB.Create(&settings).Error; err != nil {
		log.Fatalf("cannot create default polling settings: %v", err)
	}

	return settings, true
}

func LoadPollingSettings() PollingSettings {
	settings := NewPollingSettings()
	var existing PollingSettings
	if err := DB.First(&existing, 1).Error; err != nil {
		return settings
	}
	existing.Normalize()
	return existing
}

func RescheduleActiveLinksFromSettings(settings PollingSettings) {
	now := time.Now().UTC()
	commentNextAt := settings.CommentNextAt(now)
	metricsNextAt := settings.MetricsNextAt(now)

	if err := DB.Model(&Link{}).Where("active = ?", true).Updates(map[string]any{
		"next_scrape_at":          commentNextAt,
		"metrics_next_refresh_at": &metricsNextAt,
	}).Error; err != nil {
		log.Fatalf("cannot reschedule active links from global polling settings: %v", err)
	}
}

func SeedDefaultAdmin() {
	username := getEnv("ADMIN_USERNAME", "admin")
	password := getEnv("ADMIN_PASSWORD", "123456789")

	if err := DB.Model(&User{}).Where("role = '' OR role IS NULL").Update("role", RoleUser).Error; err != nil {
		log.Fatalf("cannot normalize user roles: %v", err)
	}

	var user User
	err := DB.Where("username = ?", username).First(&user).Error
	if err == nil {
		if !user.IsAdmin() {
			user.Role = RoleAdmin
			if err := DB.Save(&user).Error; err != nil {
				log.Fatalf("cannot promote default admin user: %v", err)
			}
			log.Printf("promoted default admin user username=%q to admin role", username)
		}
		return
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		log.Fatalf("cannot check default admin user: %v", err)
	}

	user = User{Username: username, Role: RoleAdmin}
	if err := user.SetPassword(password); err != nil {
		log.Fatalf("cannot hash default admin password: %v", err)
	}
	if err := DB.Create(&user).Error; err != nil {
		log.Fatalf("cannot create default admin user: %v", err)
	}

	log.Printf("created default admin user username=%q with encrypted password", username)
}

func EnsureResourceOwnership() {
	username := getEnv("ADMIN_USERNAME", "admin")
	var admin User
	if err := DB.Where("username = ?", username).First(&admin).Error; err != nil {
		log.Fatalf("cannot find default admin for resource ownership migration: %v", err)
	}

	if err := DB.Exec(`
		UPDATE resources
		SET user_id = created_by_id
		WHERE user_id IS NULL
		  AND created_by_id IS NOT NULL
		  AND EXISTS (SELECT 1 FROM users WHERE users.id = resources.created_by_id)
	`).Error; err != nil {
		log.Fatalf("cannot backfill resource owners from creators: %v", err)
	}

	if err := DB.Exec(`UPDATE resources SET user_id = ? WHERE user_id IS NULL`, admin.ID).Error; err != nil {
		log.Fatalf("cannot backfill resource owners to default admin: %v", err)
	}

	if err := DB.Exec(`DROP INDEX IF EXISTS idx_resource_type_hash`).Error; err != nil {
		log.Fatalf("cannot drop old resource unique index: %v", err)
	}
	if err := DB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_user_type_hash ON resources (user_id, type, value_hash)`).Error; err != nil {
		log.Fatalf("cannot create resource owner unique index: %v", err)
	}
}

func databaseDSN() string {
	if dsn := os.Getenv("DATABASE_DSN"); dsn != "" {
		return dsn
	}

	return fmt.Sprintf(
		"host=%s user=%s password=%s dbname=%s port=%s sslmode=%s TimeZone=%s",
		getEnv("DB_HOST", "localhost"),
		getEnv("DB_USER", "postgres"),
		getEnv("DB_PASSWORD", "postgres"),
		getEnv("DB_NAME", "fb_comment"),
		getEnv("DB_PORT", "5435"),
		getEnv("DB_SSLMODE", "disable"),
		getEnv("DB_TIMEZONE", "Asia/Ho_Chi_Minh"),
	)
}

func getEnv(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}
