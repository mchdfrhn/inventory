// Package main is the entry point for the inventory management system.
// It implements a clean architecture pattern with the following layers:
// - Delivery: HTTP handlers (presentation layer)
// - Usecase: Business logic
// - Repository: Data access
// - Domain: Business entities and interfaces
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"en-inventory/internal/config"
	httpdelivery "en-inventory/internal/delivery/http"
	"en-inventory/internal/delivery/http/middleware"
	"en-inventory/internal/domain"
	"en-inventory/internal/models"
	"en-inventory/internal/repository/postgres"
	"en-inventory/internal/usecase"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	pgdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func main() {
	// Load configuration
	cfg, err := config.LoadConfig()
	if err != nil {
		log.Fatal("Failed to load configuration:", err)
	}

	// Initialize logger
	logger, err := zap.NewProduction()
	if err != nil {
		log.Fatal("Failed to initialize logger:", err)
	}
	defer logger.Sync()

	// Set Gin mode
	gin.SetMode(cfg.Server.Mode)

	// Initialize dependencies
	db := initDB(cfg, logger)
	router := initRouter(logger)
	server := initServer(cfg, router)
	// Initialize repositories and usecases
	repos := initRepositories(db)
	usecases := initUsecases(repos)
	// Setup routes
	router.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status": "ok",
			"time":   time.Now().Format(time.RFC3339),
		})
	})
	// Initialize API handlers with router
	httpdelivery.NewAssetHandler(router, usecases.Asset)
	httpdelivery.NewCategoryHandler(router, usecases.Category)
	httpdelivery.NewLocationHandler(router, usecases.Location)
	httpdelivery.NewAuditLogHandler(router, usecases.AuditLog)

	// Start server with graceful shutdown
	go func() {
		logger.Info("Starting server", zap.String("address", server.Addr))
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatal("Failed to start server", zap.Error(err))
		}
	}()

	// Wait for interrupt signal to gracefully shutdown the server
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("Shutting down server...")

	// The context is used to inform the server it has 15 seconds to finish
	// the request it is currently handling
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		logger.Fatal("Server forced to shutdown:", zap.Error(err))
	}

	logger.Info("Server exiting")
}

// initDB initializes the database connection and performs automatic migrations.
// It uses GORM as the ORM and PostgreSQL as the database.
// The function will:
// 1. Establish database connection using provided configuration
// 2. Automatically migrate the schema for Asset and AssetCategory
// 3. Panic with a fatal log if any step fails
func initDB(cfg *config.Config, logger *zap.Logger) *gorm.DB {
	db, err := gorm.Open(pgdriver.Open(cfg.Database.GetDSN()), &gorm.Config{})
	if err != nil {
		logger.Fatal("Failed to connect to database", zap.Error(err))
	} // Auto migrate the schema
	err = db.AutoMigrate(&domain.Asset{}, &domain.AssetCategory{}, &models.Location{}, &domain.AuditLog{})
	if err != nil {
		logger.Fatal("Failed to migrate database", zap.Error(err))
	}

	return db
}

// initRouter sets up the Gin router with necessary middleware and CORS configuration.
// It configures:
// 1. Recovery middleware for panic handling
// 2. Custom logger middleware using zap
// 3. CORS support for cross-origin requests
// 4. Options request handling for CORS preflight
func initRouter(logger *zap.Logger) *gin.Engine {
	r := gin.New()

	// Add middleware for logging, recovery and security
	r.Use(gin.Recovery())
	r.Use(middleware.Logger(logger))
	r.Use(middleware.Recovery(logger))

	// CORS configuration for cross-origin access
	r.Use(func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, accept, origin, Cache-Control, X-Requested-With")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT, DELETE")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	})

	return r
}

// initServer creates and configures the HTTP server with appropriate timeouts and handler.
// Timeouts are configured for:
// - Read: 15 seconds for the entire request read
// - Write: 15 seconds for writing the response
// - Idle: 60 seconds for keep-alive connections
func initServer(cfg *config.Config, router *gin.Engine) *http.Server {
	return &http.Server{
		Addr:         ":" + cfg.Server.Port,
		Handler:      router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
}

// repositories holds all repository implementations.
// Each repository is responsible for data access operations for a specific domain entity.
type repositories struct {
	asset    domain.AssetRepository         // Handles asset-related database operations
	category domain.AssetCategoryRepository // Handles category-related database operations
	location domain.LocationRepository      // Handles location-related database operations
	auditLog domain.AuditLogRepository      // Handles audit log database operations
}

// initRepositories creates and initializes all repository implementations.
// It uses the provided database connection to create concrete implementations
// of the repository interfaces defined in the domain package.
func initRepositories(db *gorm.DB) *repositories {
	return &repositories{
		asset:    postgres.NewAssetRepository(db),
		category: postgres.NewAssetCategoryRepository(db),
		location: postgres.NewLocationRepository(db),
		auditLog: postgres.NewAuditLogRepository(db),
	}
}

// usecases holds all business logic implementations.
// Each usecase represents a specific business operation or service.
type usecases struct {
	Asset    domain.AssetUsecase         // Handles asset-related business logic
	Category domain.AssetCategoryUsecase // Handles category-related business logic
	Location domain.LocationUseCase      // Handles location-related business logic
	AuditLog domain.AuditLogUsecase      // Handles audit log business logic
}

// initUsecases creates and initializes all usecase implementations.
// It injects the necessary repositories into each usecase to maintain
// proper dependency injection and separation of concerns.
func initUsecases(repos *repositories) *usecases {
	// Initialize audit log usecase first
	auditLogUsecase := usecase.NewAuditLogUsecase(repos.auditLog)

	return &usecases{
		Asset:    usecase.NewAssetUsecase(repos.asset, repos.category, repos.location, auditLogUsecase),
		Category: usecase.NewAssetCategoryUsecase(repos.category),
		Location: usecase.NewLocationUseCase(repos.location),
		AuditLog: auditLogUsecase,
	}
}
